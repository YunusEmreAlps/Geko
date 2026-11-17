package web

import (
	"encoding/json"
	"net/http"
	"time"
)

func previousBudget(r *http.Request) bool { return r.URL.Query().Get("previous") == "true" }

func (d *dashboard) budgetPage(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, http.StatusOK, "budget", "Telemetry Budget",
		d.settings.TelemetryBudget.Snapshot(time.Now(), previousBudget(r)))
}

func (d *dashboard) budgetAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d.settings.TelemetryBudget.Snapshot(time.Now(), previousBudget(r)))
}
