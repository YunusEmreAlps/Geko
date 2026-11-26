package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/config"
)

func (d *dashboard) runtimeSettingsState(r *http.Request) *config.RuntimeState {
	if d.settings.Runtime == nil || d.readOnly(r) {
		return nil
	}
	s := d.settings.Runtime.Snapshot()
	return &s
}

func (d *dashboard) runtimeSettingsAPI(w http.ResponseWriter, r *http.Request) {
	s := d.runtimeSettingsState(r)
	if s == nil {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

func (d *dashboard) updateRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	if d.readOnly(r) {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	wantsJSON := strings.HasPrefix(r.URL.Path, "/api/")
	var input struct {
		Revision string  `json:"revision"`
		URL      *string `json:"trace_link_url"`
		Name     *string `json:"trace_link_name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var err error
	if wantsJSON {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input)
		if err == nil {
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				err = config.ErrSettingsInvalid
			}
		}
	} else {
		err = r.ParseForm()
		input.Revision = r.PostForm.Get("revision")
		link, name := r.PostForm.Get("trace_link_url"), r.PostForm.Get("trace_link_name")
		if r.PostForm.Has("trace_link_url") {
			input.URL = &link
		}
		if r.PostForm.Has("trace_link_name") {
			input.Name = &name
		}
	}
	if err != nil || input.URL == nil || input.Name == nil || input.Revision == "" {
		d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "trace_links", Result: "invalid"})
		http.Error(w, "Invalid settings request", http.StatusBadRequest)
		return
	}
	state, err := d.settings.Runtime.Update(r.Context(), input.Revision, *input.URL, *input.Name)
	status, result := http.StatusOK, "ok"
	switch {
	case errors.Is(err, config.ErrSettingsConflict):
		status, result = http.StatusConflict, "conflict"
	case errors.Is(err, config.ErrSettingsLocked):
		status, result = http.StatusForbidden, "locked"
	case errors.Is(err, config.ErrSettingsInvalid):
		status, result = http.StatusBadRequest, "invalid"
	case err != nil:
		status, result = http.StatusServiceUnavailable, "failed"
	}
	// Log only supported field names and the outcome, never URLs or YAML.
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "trace_links", Result: result})
	if err != nil {
		message := "Could not save configuration; previous settings remain active. Check file permissions or another configuration writer."
		if status != http.StatusServiceUnavailable {
			message = err.Error()
		}
		if wantsJSON {
			http.Error(w, message, status)
		} else {
			d.render(w, r, status, "error", "Settings not applied", struct{ Title, Message, Retry string }{
				"Settings not applied", message + " Reload Settings to review the active values before trying again.", "/settings#runtime-settings",
			})
		}
		return
	}
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	http.Redirect(w, r, "/settings#runtime-settings", http.StatusSeeOther)
}
