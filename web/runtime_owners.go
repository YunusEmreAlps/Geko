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

func (d *dashboard) runtimeOwnersState(r *http.Request) *config.OwnersState {
	if d.settings.Runtime == nil || d.readOnly(r) {
		return nil
	}
	s := d.settings.Runtime.OwnersState()
	return &s
}

func (d *dashboard) runtimeOwnersAPI(w http.ResponseWriter, r *http.Request) {
	s := d.runtimeOwnersState(r)
	if s == nil {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

func (d *dashboard) updateRuntimeOwners(w http.ResponseWriter, r *http.Request) {
	if d.settings.Runtime == nil || d.readOnly(r) {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	var input struct {
		Revision string                `json:"revision"`
		Mappings *config.OwnerMappings `json:"owners"`
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
				err = config.ErrOwnersInvalid
			}
		}
	} else {
		err = r.ParseForm()
		input.Revision = r.PostForm.Get("revision")
		if err == nil && r.PostForm.Has("owners_yaml") {
			var mapping config.OwnerMappings
			mapping, err = config.ParseOwnerMappings([]byte(r.PostForm.Get("owners_yaml")))
			input.Mappings = &mapping
		}
	}
	if err != nil || input.Revision == "" || input.Mappings == nil {
		d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "owners", Result: "invalid"})
		d.ownerUpdateError(w, r, wantsJSON, http.StatusBadRequest, config.ErrOwnersInvalid.Error())
		return
	}
	state, err := d.settings.Runtime.UpdateOwners(r.Context(), input.Revision, *input.Mappings)
	status, result := http.StatusOK, "ok"
	switch {
	case errors.Is(err, config.ErrSettingsConflict):
		status, result = http.StatusConflict, "conflict"
	case errors.Is(err, config.ErrSettingsLocked):
		status, result = http.StatusForbidden, "locked"
	case errors.Is(err, config.ErrOwnersInvalid):
		status, result = http.StatusBadRequest, "invalid"
	case err != nil:
		status, result = http.StatusServiceUnavailable, "failed"
	}
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "owners", Result: result})
	if err != nil {
		message := "Could not save ownership; previous rules remain active. Check file permissions or another configuration writer."
		if status != http.StatusServiceUnavailable {
			message = err.Error()
		}
		d.ownerUpdateError(w, r, wantsJSON, status, message)
		return
	}
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	http.Redirect(w, r, "/settings#ownership", http.StatusSeeOther)
}

func (d *dashboard) ownerUpdateError(w http.ResponseWriter, r *http.Request, json bool, status int, message string) {
	if json {
		http.Error(w, message, status)
		return
	}
	d.render(w, r, status, "error", "Owners not applied", struct{ Title, Message, Retry string }{"Owners not applied", message + " Reload Settings before trying again.", "/settings#ownership"})
}
