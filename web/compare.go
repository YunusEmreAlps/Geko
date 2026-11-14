package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
)

func (d *dashboard) compareTrace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		source, baseline, err := d.store.CompareTrace(ctx, id)
		if errors.Is(err, investigation.ErrTraceNotFound) {
			d.problem(w, r, http.StatusNotFound, "Trace not found", "This trace is unavailable in the local database.")
			return
		}
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view := struct {
			Source             investigation.TraceSummary
			Baseline           *investigation.TraceSummary
			DurationDifference string
		}{Source: source, Baseline: baseline}
		if baseline != nil && source.EntrySpan.DurationNS != nil && baseline.EntrySpan.DurationNS != nil {
			delta := *source.EntrySpan.DurationNS - *baseline.EntrySpan.DurationNS
			switch {
			case delta > 0:
				view.DurationDifference = formatDuration(&delta) + " slower"
			case delta < 0:
				delta = -delta
				view.DurationDifference = formatDuration(&delta) + " faster"
			default:
				view.DurationDifference = "Same duration"
			}
		}
		d.render(w, r, http.StatusOK, "compare", "Compare traces", view)
	})
}
