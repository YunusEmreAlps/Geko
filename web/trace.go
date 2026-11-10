package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"geko/internal/api"
	"geko/internal/investigation"
	"geko/internal/telemetry"
)

type traceView struct {
	Trace               investigation.TraceSummary
	Rows                []waterfallRow
	Selected            *investigation.Span
	Parent, First, Next string
	Window              string
	MissingSpan         bool
	ServiceMap          serviceMapView
}

type waterfallRow struct {
	investigation.Span
	URL, Signal string
	Depth       int
	X, Width    int
	Selected    bool
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
	limit, cursor, err := api.ParsePagination(params, len(telemetry.SpanID{}))
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
	d.withQuery(w, r, func(ctx context.Context) {
		view := traceView{}
		summary, page, err := d.store.Trace(ctx, id, cursor, limit)
		if errors.Is(err, investigation.ErrTraceNotFound) {
			d.problem(w, r, http.StatusNotFound, "Trace not found", "This trace is unavailable in the local database.")
			return
		}
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		path := "/traces/" + summary.TraceID
		if rawSpanID != "" {
			selectedID := hex.EncodeToString(spanID[:])
			for i := range page.Items {
				if page.Items[i].SpanID == selectedID {
					view.Selected = &page.Items[i]
					break
				}
			}
			if view.Selected == nil {
				selected, err := d.store.Span(ctx, id, spanID)
				switch {
				case errors.Is(err, investigation.ErrSpanNotFound):
					view.MissingSpan = true
				case err != nil:
					d.unavailable(w, r, ctx)
					return
				default:
					view.Selected = &selected
					if cursor == nil {
						cursor = immediatelyBefore(selected, spanID)
						summary, page, err = d.store.Trace(ctx, id, cursor, limit)
						if err != nil {
							d.unavailable(w, r, ctx)
							return
						}
					}
				}
			}
		} else if len(page.Items) != 0 {
			view.Selected = &page.Items[0]
		}
		view.Trace = summary
		// The map is independent of the waterfall page and focused span.
		_, mapPage, err := d.store.Trace(ctx, id, nil, investigation.MaxPageSize)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		mapSpans := mapPage.Items
		for mapPage.Next != nil && len(mapSpans) < mapSpanLimit {
			_, mapPage, err = d.store.Trace(ctx, id, mapPage.Next, investigation.MaxPageSize)
			if err != nil {
				d.unavailable(w, r, ctx)
				return
			}
			mapSpans = append(mapSpans, mapPage.Items...)
		}
		view.ServiceMap = serviceMap(mapSpans, view.Selected, path)
		view.ServiceMap.Truncated = mapPage.Next != nil
		view.Rows = waterfall(page.Items, summary.Window, view.Selected, path, cursor)
		if summary.Window != nil {
			ns := summary.Window.End.Sub(summary.Window.Start).Nanoseconds()
			view.Window = formatDuration(&ns)
		}
		if view.Selected != nil && view.Selected.ParentID != "" {
			view.Parent = spanLink(path, view.Selected.ParentID, nil)
		}
		if cursor != nil {
			view.First = path
		}
		if page.Next != nil {
			view.Next = route(path, url.Values{"cursor": {api.EncodeCursor(page.Next)}})
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

// Span IDs sort bytewise. The previous possible ID at the same timestamp makes
// the selected span the first row returned by the exclusive cursor.
func immediatelyBefore(span investigation.Span, id telemetry.SpanID) *investigation.Cursor {
	for i := len(id) - 1; i >= 0; i-- {
		id[i]--
		if id[i] != 255 {
			break
		}
	}
	return &investigation.Cursor{TimeNS: span.Start.UnixNano(), ID: id[:]}
}

func spanLink(path, span string, cursor *investigation.Cursor) string {
	params := url.Values{"span": {span}}
	if cursor != nil {
		params.Set("cursor", api.EncodeCursor(cursor))
	}
	return route(path, params) + "#span-details"
}

func evidenceLink(trace investigation.TraceSummary) string {
	path := "/traces/" + trace.TraceID
	if trace.Evidence == nil {
		return path
	}
	return spanLink(path, trace.Evidence.SpanID, nil)
}

func waterfall(spans []investigation.Span, window *investigation.TraceWindow, selected *investigation.Span, path string, cursor *investigation.Cursor) []waterfallRow {
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
			Span: span, URL: spanLink(path, span.SpanID, cursor), Signal: signal(span), Depth: min(depth, 6),
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
