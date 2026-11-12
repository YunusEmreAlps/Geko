// Package api serves Geko's local investigation API.
package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type Reader interface {
	Failures(context.Context, investigation.FailureFilter) (investigation.Page[investigation.TraceSummary], error)
	Trace(context.Context, telemetry.TraceID, *investigation.Cursor, int) (investigation.TraceSummary, investigation.Page[investigation.Span], error)
}

type queryAPI struct {
	store Reader
	slots chan struct{}
}

func Register(mux *http.ServeMux, store Reader) {
	api := &queryAPI{store: store, slots: make(chan struct{}, 4)}
	mux.HandleFunc("GET /api/v1/failures", api.local(api.failures))
	mux.HandleFunc("GET /api/v1/traces/{traceID}", api.local(api.trace))
}

func (api *queryAPI) local(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// Reject DNS rebinding requests even when they reach the loopback socket.
		if !LocalHost(r.Host) {
			fail(w, http.StatusForbidden, "query API requires a loopback Host")
			return
		}
		select {
		case api.slots <- struct{}{}:
			defer func() { <-api.slots }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, http.StatusServiceUnavailable, "query API is busy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		handler(w, r.WithContext(ctx))
	}
}

func LocalHost(host string) bool {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback() && addr.Zone() == ""
}

func (api *queryAPI) failures(w http.ResponseWriter, r *http.Request) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid query string")
		return
	}
	filter, err := ParseFailureFilter(params)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := api.store.Failures(r.Context(), filter)
	if err != nil {
		queryError(w, r.Context(), err)
		return
	}
	respond(w, http.StatusOK, pageResponse[investigation.TraceSummary]{
		Items: page.Items, NextCursor: EncodeCursor(page.Next),
	})
}

func (api *queryAPI) trace(w http.ResponseWriter, r *http.Request) {
	id, err := ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid query string")
		return
	}
	limit, cursor, err := ParsePagination(params, len(telemetry.SpanID{}))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	summary, page, err := api.store.Trace(r.Context(), id, cursor, limit)
	if err != nil {
		queryError(w, r.Context(), err)
		return
	}
	respond(w, http.StatusOK, struct {
		Trace      investigation.TraceSummary `json:"trace"`
		Spans      []investigation.Span       `json:"spans"`
		NextCursor string                     `json:"next_cursor,omitempty"`
	}{summary, page.Items, EncodeCursor(page.Next)})
}

func ParseTraceID(raw string) (telemetry.TraceID, error) {
	var id telemetry.TraceID
	if len(raw) == hex.EncodedLen(len(id)) {
		if _, err := hex.Decode(id[:], []byte(raw)); err == nil && id != (telemetry.TraceID{}) {
			return id, nil
		}
	}
	return telemetry.TraceID{}, errors.New("trace ID must be 32 hexadecimal characters and nonzero")
}

type pageResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func ParseFailureFilter(params url.Values) (investigation.FailureFilter, error) {
	var filter investigation.FailureFilter
	limit, cursor, err := ParsePagination(params, len(telemetry.TraceID{}))
	if err != nil {
		return filter, err
	}
	filter.Limit, filter.After = limit, cursor
	filter.Service, filter.Search = params.Get("service"), params.Get("q")
	filter.EvidenceService = params.Get("evidence_service")
	if value := params.Get("application"); value != "" {
		name, grouped := strings.CutPrefix(value, "group:")
		if value == "ungrouped" {
			name = ""
		} else if !grouped || name == "" || len(name) > 256 {
			return filter, errors.New("application must be ungrouped or group:<name> (up to 256 bytes)")
		}
		filter.Application = &name
	}
	if len(filter.Service) > 256 || len(filter.EvidenceService) > 256 {
		return filter, errors.New("service and evidence_service must each be at most 256 bytes")
	}
	if len(filter.Search) > 4096 {
		return filter, errors.New("q must be at most 4096 bytes")
	}
	if value := params.Get("status"); value != "" {
		code, err := strconv.Atoi(value)
		if err != nil || code < 100 || code > 599 {
			return filter, errors.New("status must be an HTTP status from 100 to 599")
		}
		filter.HTTPStatus = code
	}
	filter.From, err = parseTime(params.Get("from"))
	if err != nil {
		return filter, errors.New("from must be an RFC3339 timestamp within the nanosecond range")
	}
	filter.To, err = parseTime(params.Get("to"))
	if err != nil {
		return filter, errors.New("to must be an RFC3339 timestamp within the nanosecond range")
	}
	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		return filter, errors.New("from must precede to")
	}
	return filter, nil
}

func parseTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	if !t.Equal(time.Unix(0, t.UnixNano())) {
		return nil, errors.New("timestamp outside the nanosecond range")
	}
	return &t, nil
}

func ParsePagination(params url.Values, idSize int) (int, *investigation.Cursor, error) {
	limit := investigation.DefaultPageSize
	if value := params.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > investigation.MaxPageSize {
			return 0, nil, errors.New("limit must be between 1 and 100")
		}
	}
	value := params.Get("cursor")
	if value == "" {
		return limit, nil, nil
	}
	stamp, encodedID, ok := strings.Cut(value, ":")
	if !ok || len(encodedID) != hex.EncodedLen(idSize) {
		return 0, nil, errors.New("invalid cursor")
	}
	ns, timeErr := strconv.ParseInt(stamp, 10, 64)
	id, idErr := hex.DecodeString(encodedID)
	if timeErr != nil || idErr != nil {
		return 0, nil, errors.New("invalid cursor")
	}
	return limit, &investigation.Cursor{TimeNS: ns, ID: id}, nil
}

func EncodeCursor(cursor *investigation.Cursor) string {
	if cursor == nil {
		return ""
	}
	return strconv.FormatInt(cursor.TimeNS, 10) + ":" + hex.EncodeToString(cursor.ID)
}

func queryError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case errors.Is(err, investigation.ErrTraceNotFound):
		fail(w, http.StatusNotFound, "trace not found")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fail(w, http.StatusGatewayTimeout, "query timed out")
	default:
		fail(w, http.StatusInternalServerError, "query failed")
	}
}

func fail(w http.ResponseWriter, code int, message string) {
	respond(w, code, struct {
		Error string `json:"error"`
	}{message})
}

func respond(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
