package web

import (
	"context"
	"net/http"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
)

type runsView struct {
	Baseline, Candidate, Error string
	Result                     *investigation.RunComparison
}

func (d *dashboard) compareRuns(w http.ResponseWriter, r *http.Request) {
	view := runsView{Baseline: r.URL.Query().Get("baseline"), Candidate: r.URL.Query().Get("candidate")}
	status := http.StatusOK
	if view.Baseline != "" || view.Candidate != "" {
		if !investigation.ValidRunID(view.Baseline) || !investigation.ValidRunID(view.Candidate) || view.Baseline == view.Candidate {
			view.Error = "Choose two different run IDs of 32 lowercase hexadecimal characters."
			status = http.StatusBadRequest
		} else if reader, ok := d.store.(api.RunReader); ok {
			d.withQuery(w, r, func(ctx context.Context) {
				result, err := reader.CompareRuns(ctx, view.Baseline, view.Candidate)
				if err != nil {
					view.Error = "Run comparison could not be collected. Try again; no fix is inferred."
					status = http.StatusServiceUnavailable
				} else {
					view.Result = &result
				}
				d.render(w, r, status, "runs", "Compare runs", view)
			})
			return
		} else {
			view.Error = "Run comparison is unavailable on this server."
			status = http.StatusServiceUnavailable
		}
	}
	d.render(w, r, status, "runs", "Compare runs", view)
}
