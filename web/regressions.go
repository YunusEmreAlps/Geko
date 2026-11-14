package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

type regressionRow struct {
	investigation.VersionComparison
	BaselineRate, CandidateRate string
	Signal                      string
}

func (d *dashboard) regressions(w http.ResponseWriter, r *http.Request) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", "Check the version comparison filters.")
		return
	}
	view := struct {
		Baseline, Candidate, Service, Range string
		Rows                                []regressionRow
		Compared, Truncated                 bool
	}{
		Baseline: strings.TrimSpace(params.Get("baseline")), Candidate: strings.TrimSpace(params.Get("candidate")),
		Service: strings.TrimSpace(params.Get("service")), Range: params.Get("range"),
	}
	if view.Range == "" {
		view.Range = "24h"
	}
	window, ok := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}[view.Range]
	if !ok || len(view.Baseline) > 256 || len(view.Candidate) > 256 || len(view.Service) > 256 {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", "Use versions and service names up to 256 bytes and an available time range.")
		return
	}
	if view.Baseline == "" && view.Candidate == "" {
		d.render(w, r, http.StatusOK, "regressions", "Version regressions", view)
		return
	}
	if view.Baseline == "" || view.Candidate == "" || view.Baseline == view.Candidate {
		d.problem(w, r, http.StatusBadRequest, "Invalid versions", "Enter two different service versions.")
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		rows, err := d.store.VersionComparisons(ctx, investigation.RegressionFilter{
			Baseline: view.Baseline, Candidate: view.Candidate, Service: view.Service, Since: time.Now().Add(-window),
		})
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view.Compared = true
		view.Truncated = len(rows) > 50
		for _, row := range rows[:min(len(rows), 50)] {
			signal := "No threshold crossed"
			switch {
			case row.FailureRegression && row.LatencyRegression:
				signal = "Failure rate and latency"
			case row.FailureRegression:
				signal = "Failure rate"
			case row.LatencyRegression:
				signal = "Latency"
			case min(row.Baseline.Traces, row.Candidate.Traces) < investigation.RegressionMinSamples:
				signal = "Too few requests"
			case min(row.Baseline.Timed, row.Candidate.Timed) < investigation.RegressionMinSamples:
				signal = "Too few valid durations"
			}
			view.Rows = append(view.Rows, regressionRow{row,
				fmt.Sprintf("%.1f%%", 100*float64(row.Baseline.Failures)/float64(row.Baseline.Traces)),
				fmt.Sprintf("%.1f%%", 100*float64(row.Candidate.Failures)/float64(row.Candidate.Traces)), signal})
		}
		d.render(w, r, http.StatusOK, "regressions", "Version regressions", view)
	})
}
