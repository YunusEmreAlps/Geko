// Package otlp receives OpenTelemetry trace exports over HTTP protobuf.
package otlp

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

const (
	protobufContentType = "application/x-protobuf"
	maxPayloadBytes     = 4 << 20
	maxConcurrent       = 4
	writeTimeout        = 8 * time.Second
)

type spanWriter interface {
	WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error
}

type receiver struct {
	store spanWriter
	slots chan struct{}
}

func NewHandler(store spanWriter) http.Handler {
	return &receiver{store: store, slots: make(chan struct{}, maxConcurrent)}
}

func (h *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != protobufContentType {
		fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument, "expected application/x-protobuf")
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument, "expected identity or gzip content encoding")
		return
	}

	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		unavailable(w, "trace receiver is busy")
		return
	}

	payload, err := readPayload(w, r, encoding)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "trace payload exceeds 4 MiB")
		} else {
			fail(w, http.StatusBadRequest, codes.InvalidArgument, "cannot read trace payload")
		}
		return
	}
	var req collector.ExportTraceServiceRequest
	if err := (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 32}).Unmarshal(payload, &req); err != nil {
		fail(w, http.StatusBadRequest, codes.InvalidArgument, "invalid OTLP trace protobuf")
		return
	}
	spans, err := normalize(&req)
	switch {
	case errors.Is(err, errBatchLimit):
		fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, err.Error())
		return
	case err != nil:
		fail(w, http.StatusBadRequest, codes.InvalidArgument, err.Error())
		return
	}

	if len(spans) != 0 {
		ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
		defer cancel()
		if err := h.store.WriteSpans(ctx, time.Now(), spans); err != nil {
			unavailable(w, "trace storage unavailable")
			return
		}
	}
	respond(w, http.StatusOK, &collector.ExportTraceServiceResponse{})
}

func readPayload(w http.ResponseWriter, r *http.Request, encoding string) ([]byte, error) {
	if r.ContentLength > maxPayloadBytes {
		return nil, &http.MaxBytesError{Limit: maxPayloadBytes}
	}
	wire := http.MaxBytesReader(w, r.Body, maxPayloadBytes)
	defer wire.Close()
	if encoding != "gzip" {
		return io.ReadAll(wire)
	}

	decoded, err := gzip.NewReader(wire)
	if err != nil {
		return nil, err
	}
	defer decoded.Close()
	payload, err := io.ReadAll(io.LimitReader(decoded, maxPayloadBytes+1))
	if len(payload) > maxPayloadBytes {
		return nil, &http.MaxBytesError{Limit: maxPayloadBytes}
	}
	return payload, err
}

func unavailable(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", "1")
	fail(w, http.StatusServiceUnavailable, codes.Unavailable, message)
}

func fail(w http.ResponseWriter, httpCode int, code codes.Code, message string) {
	respond(w, httpCode, &status.Status{Code: int32(code), Message: message})
}

func respond(w http.ResponseWriter, httpCode int, msg proto.Message) {
	body, err := proto.Marshal(msg)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", protobufContentType)
	w.WriteHeader(httpCode)
	_, _ = w.Write(body)
}
