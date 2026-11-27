package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
)

func (d *dashboard) runtimeBudgetState(r *http.Request) *config.BudgetState {
	if d.settings.Runtime == nil || d.readOnly(r) {
		return nil
	}
	s := d.settings.Runtime.BudgetState()
	return &s
}

func (d *dashboard) runtimeBudgetAPI(w http.ResponseWriter, r *http.Request) {
	s := d.runtimeBudgetState(r)
	if s == nil {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

func (d *dashboard) updateRuntimeBudget(w http.ResponseWriter, r *http.Request) {
	if d.settings.Runtime == nil || d.readOnly(r) {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	var input struct {
		Revision string                  `json:"revision"`
		Limits   *budget.ReportingLimits `json:"limits"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	wantsJSON := strings.HasPrefix(r.URL.Path, "/api/")
	var err error
	if wantsJSON {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = config.ErrBudgetInvalid
			}
		}
	} else {
		err = r.ParseForm()
		input.Revision = r.PostForm.Get("revision")
		if err == nil && r.PostForm.Has("budget_yaml") {
			var mapping budget.ReportingLimits
			mapping, err = config.ParseBudgetLimits([]byte(r.PostForm.Get("budget_yaml")))
			input.Limits = &mapping
		}
	}
	if err != nil || input.Revision == "" || input.Limits == nil {
		d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "budget_limits", Result: "invalid"})
		d.budgetUpdateError(w, r, wantsJSON, http.StatusBadRequest, config.ErrBudgetInvalid.Error())
		return
	}
	state, err := d.settings.Runtime.UpdateBudget(r.Context(), input.Revision, *input.Limits)
	status, result := http.StatusOK, "ok"
	switch {
	case errors.Is(err, config.ErrSettingsConflict):
		status, result = http.StatusConflict, "conflict"
	case errors.Is(err, config.ErrSettingsLocked):
		status, result = http.StatusForbidden, "locked"
	case errors.Is(err, config.ErrBudgetInvalid):
		status, result = http.StatusBadRequest, "invalid"
	case err != nil:
		status, result = http.StatusServiceUnavailable, "failed"
	}
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "budget_limits", Result: result})
	if err != nil {
		message := "Could not save Budget limits; previous limits remain active. Check file permissions or another configuration writer."
		if status != http.StatusServiceUnavailable {
			message = err.Error()
		}
		d.budgetUpdateError(w, r, wantsJSON, status, message)
		return
	}
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	http.Redirect(w, r, "/settings#budget-limits", http.StatusSeeOther)
}

func (d *dashboard) budgetUpdateError(w http.ResponseWriter, r *http.Request, json bool, status int, message string) {
	if json {
		http.Error(w, message, status)
		return
	}
	d.render(w, r, status, "error", "Budget limits not applied", struct{ Title, Message, Retry string }{"Budget limits not applied", message + " Reload Settings before trying again.", "/settings#budget-limits"})
}
