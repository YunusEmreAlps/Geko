package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// metrics serves Prometheus text exposition about Geko itself. It reports no
// telemetry content, only receiver counters and storage sizes.
func (d *dashboard) metrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	status, err := d.store.Status(ctx)
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	var b strings.Builder
	metric := func(name, kind, help string, samples ...string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
		for _, sample := range samples {
			b.WriteString(name + sample + "\n")
		}
	}
	metric("geko_build_info", "gauge", "Geko build version.", fmt.Sprintf(`{version=%q} 1`, d.settings.Version))
	if snapshot := d.settings.ReceiverDiagnostics; snapshot != nil {
		diagnostics := snapshot()
		metric("geko_receiver_accepted_exports_total", "counter", "Exports with spans accepted since start.", fmt.Sprintf(" %d", diagnostics.AcceptedExports))
		metric("geko_receiver_rejected_spans_total", "counter", "Spans rejected in partially accepted exports since start.", fmt.Sprintf(" %d", diagnostics.RejectedSpans))
		var rejected []string
		for _, rejection := range diagnostics.Rejections {
			rejected = append(rejected, fmt.Sprintf(`{reason=%q} %d`, metricLabel(rejection.Reason), rejection.Count))
		}
		metric("geko_receiver_rejected_requests_total", "counter", "Export requests rejected since start, by reason.", rejected...)
		if !diagnostics.LastAccepted.IsZero() {
			metric("geko_receiver_last_accepted_timestamp_seconds", "gauge", "Time of the last accepted export.",
				fmt.Sprintf(" %.3f", float64(diagnostics.LastAccepted.UnixMilli())/1000))
		}
	}
	metric("geko_storage_bytes", "gauge", "SQLite file sizes, by file.",
		fmt.Sprintf(`{file="main"} %d`, status.MainBytes), fmt.Sprintf(`{file="wal"} %d`, status.WALBytes), fmt.Sprintf(`{file="shm"} %d`, status.SHMBytes))
	metric("geko_storage_free_bytes", "gauge", "Reusable space in the main database file.", fmt.Sprintf(" %d", status.FreeBytes))
	if d.settings.MaxDBBytes > 0 {
		metric("geko_storage_limit_bytes", "gauge", "Configured main database size limit.", fmt.Sprintf(" %d", d.settings.MaxDBBytes))
	}
	metric("geko_storage_evicted_traces_total", "counter", "Traces evicted at capacity.", fmt.Sprintf(" %d", status.EvictedTraces))
	metric("geko_storage_rejected_exports_total", "counter", "Exports rejected at capacity.", fmt.Sprintf(" %d", status.RejectedExports))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func metricLabel(reason string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		default:
			return '_'
		}
	}, reason)
}
