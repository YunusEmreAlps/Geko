package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type traceView struct {
	Trace               investigation.TraceSummary
	Rows                []waterfallRow
	Selected            *investigation.Span
	Parent, First, Next string
	Window              string
	MissingSpan         bool
	ServiceMap          serviceMapView
	Evidence            []evidenceRow
	EvidenceFirst       string
	EvidenceNext        string
}

type evidenceRow struct {
	investigation.Evidence
	URL      string
	Selected bool
}

type waterfallRow struct {
	investigation.Span
	URL, Signal string
	Depth       int
	X, Width    int
	Selected    bool
}

func (d *dashboard) findTrace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(strings.TrimSpace(r.URL.Query().Get("trace_id")))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	http.Redirect(w, r, "/traces/"+hex.EncodeToString(id[:]), http.StatusSeeOther)
}

func (d *dashboard) trace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace link", "Open the trace again from the failures page.")
		return
	}
	params.Set("limit", "50")
	_, cursor, err := api.ParsePagination(params, len(telemetry.SpanID{}))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace page", err.Error())
		return
	}
	rawSpanID := params.Get("span")
	spanID, valid := parseSpanID(rawSpanID)
	if rawSpanID != "" && !valid {
		d.problem(w, r, http.StatusBadRequest, "Invalid span ID", "A span ID must contain 16 hexadecimal characters and be nonzero.")
		return
	}
	var evidenceAfter *telemetry.SpanID
	if raw := params.Get("evidence_after"); raw != "" {
		anchor, ok := parseSpanID(raw)
		if !ok {
			d.problem(w, r, http.StatusBadRequest, "Invalid evidence page", "Open the trace again from the failures page.")
			return
		}
		evidenceAfter = &anchor
	}
	d.withQuery(w, r, func(ctx context.Context) {
		view := traceView{}
		filter := investigation.TraceDetailFilter{After: cursor, EvidenceAfter: evidenceAfter}
		if rawSpanID != "" {
			filter.Selected = &spanID
		}
		details, err := d.store.TraceDetails(ctx, id, filter)
		if errors.Is(err, investigation.ErrTraceNotFound) {
			d.problem(w, r, http.StatusNotFound, "Trace not found", "This trace is unavailable in the local database.")
			return
		}
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		summary, page, cursor := details.Summary, details.Waterfall, details.After
		path := "/traces/" + summary.TraceID
		view.Selected = details.Selected
		view.MissingSpan = filter.Selected != nil && view.Selected == nil
		view.Trace = summary
		if summary.Evidence != nil {
			links := maps.Clone(params)
			links.Del("limit")
			for _, item := range details.Evidence {
				link := maps.Clone(links)
				link.Del("cursor")
				link.Set("span", item.SpanID)
				view.Evidence = append(view.Evidence, evidenceRow{
					Evidence: item, URL: route(path, link) + "#span-details",
					Selected: view.Selected != nil && view.Selected.SpanID == item.SpanID,
				})
			}
			if evidenceAfter != nil {
				links.Del("evidence_after")
				view.EvidenceFirst = route(path, links) + "#evidence"
			}
			if details.EvidenceNext != "" {
				links.Set("evidence_after", details.EvidenceNext)
				view.EvidenceNext = route(path, links) + "#evidence"
			}
		}
		mergeEnvironments := params.Get("environments") == "merged"
		view.ServiceMap = serviceMap(details.MapSpans, view.Selected, path, mergeEnvironments)
		toggle := maps.Clone(params)
		toggle.Del("limit")
		if mergeEnvironments {
			toggle.Del("environments")
		} else {
			toggle.Set("environments", "merged")
		}
		view.ServiceMap.EnvironmentToggle = route(path, toggle) + "#service-map"
		view.ServiceMap.Truncated = summary.Window != nil && summary.Window.SpanCount > len(details.MapSpans)
		view.Rows = waterfall(page.Items, summary.Window, view.Selected, path, cursor, mergeEnvironments)
		if summary.Window != nil {
			ns := summary.Window.End.Sub(summary.Window.Start).Nanoseconds()
			view.Window = formatDuration(&ns)
		}
		if view.Selected != nil && view.Selected.ParentID != "" {
			view.Parent = spanLink(path, view.Selected.ParentID, nil, mergeEnvironments)
		}
		if cursor != nil {
			view.First = traceRoute(path, url.Values{}, mergeEnvironments)
		}
		if page.Next != nil {
			view.Next = traceRoute(path, url.Values{"cursor": {api.EncodeCursor(page.Next)}}, mergeEnvironments)
		}
		d.render(w, http.StatusOK, "trace", "Trace details", view)
	})
}

func parseSpanID(raw string) (telemetry.SpanID, bool) {
	var id telemetry.SpanID
	if len(raw) != hex.EncodedLen(len(id)) {
		return id, false
	}
	_, err := hex.Decode(id[:], []byte(raw))
	return id, err == nil && id != (telemetry.SpanID{})
}

func traceRoute(path string, params url.Values, mergeEnvironments bool) string {
	if mergeEnvironments {
		params.Set("environments", "merged")
	}
	return route(path, params)
}

func spanLink(path, span string, cursor *investigation.Cursor, mergeEnvironments bool) string {
	params := url.Values{"span": {span}}
	if cursor != nil {
		params.Set("cursor", api.EncodeCursor(cursor))
	}
	return traceRoute(path, params, mergeEnvironments) + "#span-details"
}

func evidenceLink(trace investigation.TraceSummary) string {
	path := "/traces/" + trace.TraceID
	if trace.Evidence == nil {
		return path
	}
	return spanLink(path, trace.Evidence.SpanID, nil, false)
}

func waterfall(spans []investigation.Span, window *investigation.TraceWindow, selected *investigation.Span, path string, cursor *investigation.Cursor, mergeEnvironments bool) []waterfallRow {
	present := make(map[string]bool, len(spans))
	children := make(map[string][]investigation.Span, len(spans))
	for _, span := range spans {
		present[span.SpanID] = true
		children[span.ParentID] = append(children[span.ParentID], span)
	}
	rows := make([]waterfallRow, 0, len(spans))
	seen := make(map[string]bool, len(spans))
	var add func(investigation.Span, int)
	add = func(span investigation.Span, depth int) {
		if seen[span.SpanID] {
			return
		}
		seen[span.SpanID] = true
		x, width := barPosition(span, window)
		rows = append(rows, waterfallRow{
			Span: span, URL: spanLink(path, span.SpanID, cursor, mergeEnvironments), Signal: signal(span), Depth: min(depth, 6),
			X: x, Width: width, Selected: selected != nil && span.SpanID == selected.SpanID,
		})
		for _, child := range children[span.SpanID] {
			add(child, depth+1)
		}
	}
	for _, span := range spans {
		if !present[span.ParentID] {
			add(span, 0)
		}
	}
	for _, span := range spans {
		add(span, 0) // Keep rows visible if malformed parent links form a cycle.
	}
	return rows
}

func barPosition(span investigation.Span, window *investigation.TraceWindow) (int, int) {
	if window == nil || !window.End.After(window.Start) {
		return 0, 4
	}
	total := float64(window.End.Sub(window.Start))
	offset := int(1000 * float64(span.Start.Sub(window.Start)) / total)
	end := int(1000 * float64(span.End.Sub(window.Start)) / total)
	x := min(max(offset, 0), 996)
	return x, min(max(end-x, 4), 1000-x)
}

func signal(span investigation.Span) string {
	for _, event := range span.Events {
		if event.Name == "exception" {
			return "exception"
		}
	}
	if span.HTTPStatus != nil && *span.HTTPStatus >= 500 {
		return "http_5xx"
	}
	if span.StatusCode == telemetry.StatusError {
		return "span_error"
	}
	return ""
}

func classification(value string) string {
	switch value {
	case "exception":
		return "Exception"
	case "http_5xx":
		return "HTTP 5xx"
	case "span_error":
		return "Span error"
	default:
		return "None observed"
	}
}

func outcome(trace investigation.TraceSummary) string {
	if trace.EntrySpan == nil || trace.EntrySpan.HTTPStatus == nil {
		return "Unknown"
	}
	return fmt.Sprintf("HTTP %d", *trace.EntrySpan.HTTPStatus)
}

func outcomeClass(trace investigation.TraceSummary) string {
	if trace.EntrySpan != nil && trace.EntrySpan.HTTPStatus != nil {
		code := *trace.EntrySpan.HTTPStatus
		if code >= 400 && code < 500 {
			return "client_error"
		}
	}
	return trace.HTTPOutcome
}

func formatTimestamp(t time.Time) string {
	return t.UTC().Format("02 Jan 2006, 15:04:05 UTC")
}

func formatDuration(ns *int64) string {
	if ns == nil {
		return "Unavailable"
	}
	d := time.Duration(*ns)
	switch {
	case d >= time.Second:
		d = d.Round(time.Millisecond)
	case d >= time.Millisecond:
		d = d.Round(time.Microsecond)
	}
	return d.String()
}

func formatSize(n int64) string {
	const kib, mib, gib = 1 << 10, 1 << 20, 1 << 30
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
