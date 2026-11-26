package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func (d *dashboard) compareTrace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		var compared investigation.TraceComparison
		var err error
		if reader, ok := d.store.(interface {
			TraceComparison(context.Context, telemetry.TraceID) (investigation.TraceComparison, error)
		}); ok {
			compared, err = reader.TraceComparison(ctx, id)
		} else {
			compared.Source, compared.Baseline, err = d.store.CompareTrace(ctx, id)
		}
		source, baseline := compared.Source, compared.Baseline
		if errors.Is(err, investigation.ErrTraceNotFound) {
			d.traceMissing(w, r, r.PathValue("traceID"))
			return
		}
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view := struct {
			Source                 investigation.TraceSummary
			Baseline               *investigation.TraceSummary
			DurationDifference     string
			SourceURL, BaselineURL string
			Operations             []spanOperationRow
			Truncated              bool
			SpanLimit              int
			OperationCount         int
		}{Source: source, Baseline: baseline}
		back := r.URL.Query().Get("return")
		view.SourceURL = withReturn(evidenceLink(source), back)
		view.Truncated, view.SpanLimit = compared.Truncated, investigation.MapSpanLimit
		if baseline != nil {
			view.BaselineURL = withReturn(evidenceLink(*baseline), back)
			view.Operations = comparisonRows(compared.SourceSpans, compared.BaselineSpans)
			view.OperationCount = len(view.Operations)
			view.Operations = view.Operations[:min(len(view.Operations), 100)]
			for i := range view.Operations {
				row := &view.Operations[i]
				if row.Source.Calls > 0 {
					row.SourceURL = withReturn("/traces/"+source.TraceID+"?span="+row.Source.SpanID+"#span-details", back)
				}
				if row.Baseline.Calls > 0 {
					row.BaselineURL = withReturn("/traces/"+baseline.TraceID+"?span="+row.Baseline.SpanID+"#span-details", back)
				}
			}
		}
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
