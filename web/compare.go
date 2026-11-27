package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type manualComparisonReader interface {
	TraceComparisonWithBaseline(context.Context, telemetry.TraceID, telemetry.TraceID) (investigation.TraceComparison, error)
}

func (d *dashboard) compareTrace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	params, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid comparison query", "Open the comparison again from its source trace.")
		return
	}
	values, manual := params["baseline"]
	var selected telemetry.TraceID
	selection := ""
	if manual {
		if len(values) != 1 {
			d.problem(w, r, http.StatusBadRequest, "Invalid baseline ID", "Provide one 32-character trace ID.")
			return
		}
		selected, err = api.ParseTraceID(strings.TrimSpace(values[0]))
		if err != nil {
			d.problem(w, r, http.StatusBadRequest, "Invalid baseline ID", err.Error())
			return
		}
		selection = strings.ToLower(strings.TrimSpace(values[0]))
	}
	selector, canSelect := d.store.(manualComparisonReader)
	if manual && !canSelect {
		d.problem(w, r, http.StatusNotImplemented, "Manual comparison unavailable", "This reader supports automatic matching only.")
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		var compared investigation.TraceComparison
		var err error
		if manual {
			compared, err = selector.TraceComparisonWithBaseline(ctx, id, selected)
		} else if reader, ok := d.store.(interface {
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
		status, selectionError := http.StatusOK, ""
		switch {
		case errors.Is(err, investigation.ErrBaselineNotFound):
			status, selectionError = http.StatusNotFound, err.Error()
		case errors.Is(err, investigation.ErrBaselineIneligible), errors.Is(err, investigation.ErrComparisonSource):
			status, selectionError = http.StatusBadRequest, err.Error()
		case err != nil:
			d.unavailable(w, r, ctx)
			return
		}
		view := struct {
			Source                                                   investigation.TraceSummary
			Baseline                                                 *investigation.TraceSummary
			DurationDifference                                       string
			SourceURL, BaselineURL                                   string
			Operations                                               []spanOperationRow
			Truncated                                                bool
			SpanLimit                                                int
			OperationCount                                           int
			CanSelect, Manual                                        bool
			Selection, SelectionError, Path, ReturnURL, AutomaticURL string
		}{Source: source, Baseline: baseline, CanSelect: canSelect, Manual: manual, Selection: selection, SelectionError: selectionError, Path: "/traces/" + source.TraceID + "/compare"}
		back := params.Get("return")
		if back != "" {
			back = localReturnURL(back)
		}
		view.ReturnURL = back
		view.AutomaticURL = withReturn(view.Path, back)
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
		d.render(w, r, status, "compare", "Compare traces", view)
	})
}
