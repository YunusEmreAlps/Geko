package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
)

func previousBudget(r *http.Request) bool { return r.URL.Query().Get("previous") == "true" }

var errBudgetHour = errors.New("invalid or conflicting UTC hour")

func (d *dashboard) budgetPage(w http.ResponseWriter, r *http.Request) {
	d.withQuery(w, r, func(ctx context.Context) {
		report, err := d.budgetReport(ctx, r)
		if err != nil {
			if errors.Is(err, errBudgetHour) {
				d.problem(w, r, http.StatusBadRequest, "Invalid UTC hour", "Choose a whole UTC hour, and use hour or previous, not both.")
				return
			}
			d.problem(w, r, http.StatusServiceUnavailable, "Budget history unavailable", "Check the requested UTC hour and database; no missing measurements are assumed to be zero.")
			return
		}
		d.render(w, r, http.StatusOK, "budget", "Telemetry Budget", report)
	})
}

func (d *dashboard) budgetAPI(w http.ResponseWriter, r *http.Request) {
	d.withQuery(w, r, func(ctx context.Context) {
		report, err := d.budgetReport(ctx, r)
		if err != nil {
			if errors.Is(err, errBudgetHour) {
				http.Error(w, "choose one whole UTC hour", http.StatusBadRequest)
				return
			}
			http.Error(w, "budget history unavailable or invalid UTC hour", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	})
}

func (d *dashboard) budgetReport(ctx context.Context, r *http.Request) (budget.Report, error) {
	now := time.Now()
	hour := now.UTC().Truncate(time.Hour)
	if previousBudget(r) {
		hour = hour.Add(-time.Hour)
	}
	if raw := r.URL.Query().Get("hour"); raw != "" {
		var err error
		hour, err = budget.ParseHour(raw)
		if err != nil || previousBudget(r) {
			return budget.Report{}, errBudgetHour
		}
	}
	reader, _ := d.store.(budget.HistoryReader)
	if d.settings.TelemetryBudget.HistoryDays() > 0 && reader == nil {
		return budget.Report{}, errors.New("history storage unavailable")
	}
	return d.settings.TelemetryBudget.ReportAt(ctx, reader, now, hour)
}
