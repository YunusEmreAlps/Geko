package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

type operationField struct {
	Name, Label, Value, Type, Min, Max string
	Locked                             bool
	Source                             config.SettingSource
}
type operationsSettings struct {
	config.OperationsState
	Fields []operationField
}

func operationFields(state config.OperationsState) []operationField {
	p := state.Policy
	fields := []operationField{{Name: "retention_days", Label: "Retention days", Value: strconv.Itoa(p.RetentionDays), Type: "number", Min: "1", Max: "3650"}, {Name: "max_db_mib", Label: "Main database limit (MiB)", Value: strconv.Itoa(p.MaxDBMiB), Type: "number", Min: "8", Max: "65536"}, {Name: "ingest_concurrency", Label: "Concurrent exports", Value: strconv.Itoa(p.IngestConcurrency), Type: "number", Min: "1", Max: "64"}, {Name: "on_full", Label: "When storage is full", Value: string(p.OnFull), Type: "text"}, {Name: "redaction", Label: "Exception redaction", Value: p.Redaction, Type: "text"}, {Name: "budget_history_days", Label: "Budget history days", Value: strconv.Itoa(p.BudgetHistoryDays), Type: "number", Min: "0", Max: "30"}, {Name: "digest", Label: "Weekly digest (UTC)", Value: p.Digest, Type: "text"}}
	for i := range fields {
		fields[i].Source = state.Sources[fields[i].Name]
		fields[i].Locked = !state.Editable || fields[i].Source.Kind == "env"
	}
	return fields
}
func (d *dashboard) runtimeOperationsState(r *http.Request) *operationsSettings {
	if d.settings.Runtime == nil || d.readOnly(r) {
		return nil
	}
	s := d.settings.Runtime.OperationsState()
	return &operationsSettings{s, operationFields(s)}
}
func (d *dashboard) runtimeOperationsAPI(w http.ResponseWriter, r *http.Request) {
	s := d.runtimeOperationsState(r)
	if s == nil {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.OperationsState)
}

type operationsInput struct {
	Revision  string                   `json:"revision"`
	Policy    *config.OperationsPolicy `json:"policy"`
	Confirmed bool                     `json:"confirmed"`
}

func readOperations(w http.ResponseWriter, r *http.Request) (operationsInput, error) {
	var input operationsInput
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			return input, config.ErrOperationsInvalid
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return input, config.ErrOperationsInvalid
		}
	} else {
		if r.ParseForm() != nil {
			return input, config.ErrOperationsInvalid
		}
		input.Revision = r.PostForm.Get("revision")
		input.Confirmed = r.PostForm.Get("confirmed") == "true"
		var p config.OperationsPolicy
		numbers := []struct {
			key    string
			target *int
		}{{"retention_days", &p.RetentionDays}, {"max_db_mib", &p.MaxDBMiB}, {"ingest_concurrency", &p.IngestConcurrency}, {"budget_history_days", &p.BudgetHistoryDays}}
		for _, field := range numbers {
			value, err := strconv.Atoi(r.PostForm.Get(field.key))
			if err != nil {
				return input, config.ErrOperationsInvalid
			}
			*field.target = value
		}
		p.OnFull = storage.FullPolicy(r.PostForm.Get("on_full"))
		p.Redaction = r.PostForm.Get("redaction")
		p.Digest = strings.TrimSpace(r.PostForm.Get("digest"))
		input.Policy = &p
	}
	if input.Revision == "" || input.Policy == nil {
		return input, config.ErrOperationsInvalid
	}
	return input, nil
}

type operationsPreview struct {
	Revision            string                  `json:"revision"`
	Policy              config.OperationsPolicy `json:"policy"`
	Fields              []operationField        `json:"-"`
	NeedsConfirmation   bool                    `json:"needs_confirmation"`
	BudgetCandidates    *int64                  `json:"budget_checkpoint_candidates"`
	RetentionCandidates *int64                  `json:"retention_candidates"`
	Cutoff              time.Time               `json:"retention_cutoff"`
	Storage             *storage.Status         `json:"storage,omitempty"`
}

func (d *dashboard) previewRuntimeOperations(w http.ResponseWriter, r *http.Request) {
	if d.settings.Runtime == nil || d.readOnly(r) {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	input, err := readOperations(w, r)
	if err != nil {
		d.operationError(w, r, err)
		return
	}
	state, err := d.settings.Runtime.PreviewOperations(input.Revision, *input.Policy)
	if err != nil {
		d.operationError(w, r, err)
		return
	}
	view := operationsPreview{Revision: state.Revision, Policy: *input.Policy, NeedsConfirmation: input.Policy.NeedsConfirmation(state.Policy), Cutoff: time.Now().Add(-time.Duration(input.Policy.RetentionDays) * 24 * time.Hour)}
	proposed := state
	proposed.Policy = *input.Policy
	view.Fields = operationFields(proposed)
	d.withQuery(w, r, func(ctx context.Context) {
		if reader, ok := d.store.(interface {
			RetentionPreview(context.Context, time.Time) (int64, error)
		}); ok {
			if count, err := reader.RetentionPreview(ctx, view.Cutoff); err == nil {
				view.RetentionCandidates = &count
			}
		}
		if view.Policy.BudgetHistoryDays > 0 {
			if reader, ok := d.store.(interface {
				BudgetRetentionPreview(context.Context, time.Time) (int64, error)
			}); ok {
				cutoff := time.Now().UTC().Truncate(time.Hour).Add(-time.Duration(view.Policy.BudgetHistoryDays) * 24 * time.Hour)
				if count, err := reader.BudgetRetentionPreview(ctx, cutoff); err == nil {
					view.BudgetCandidates = &count
				}
			}
		}
		if d.store != nil {
			if status, err := d.store.Status(ctx); err == nil {
				view.Storage = &status
			}
		}
		if view.NeedsConfirmation && (view.RetentionCandidates == nil || view.Storage == nil || view.Policy.BudgetHistoryDays > 0 && view.BudgetCandidates == nil) {
			d.operationError(w, r, config.ErrSettingsSave)
			return
		}
		d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.preview", Target: "operations", Result: "ok"})
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(view)
			return
		}
		d.render(w, r, http.StatusOK, "operations-preview", "Review operational changes", view)
	})
}
func (d *dashboard) updateRuntimeOperations(w http.ResponseWriter, r *http.Request) {
	if d.settings.Runtime == nil || d.readOnly(r) {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return
	}
	input, err := readOperations(w, r)
	if err != nil {
		d.operationError(w, r, err)
		return
	}
	state, err := d.settings.Runtime.UpdateOperations(r.Context(), input.Revision, *input.Policy, input.Confirmed)
	if err != nil {
		d.operationError(w, r, err)
		return
	}
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "settings.runtime.update", Target: "operations", Result: "ok"})
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	http.Redirect(w, r, "/settings#operations", http.StatusSeeOther)
}
func (d *dashboard) operationError(w http.ResponseWriter, r *http.Request, err error) {
	code, result, message := http.StatusServiceUnavailable, "failed", "Could not preview or save operational settings; previous policy remains active. Check storage and configuration access."
	switch {
	case errors.Is(err, config.ErrSettingsConflict):
		code, result, message = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, config.ErrSettingsLocked):
		code, result, message = http.StatusForbidden, "locked", err.Error()
	case errors.Is(err, config.ErrOperationsInvalid), errors.Is(err, config.ErrOperationsConfirm):
		code, result, message = http.StatusBadRequest, "invalid", err.Error()
	}
	action := "settings.runtime.update"
	if strings.HasSuffix(r.URL.Path, "/preview") {
		action = "settings.runtime.preview"
	}
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: action, Target: "operations", Result: result})
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, message, code)
		return
	}
	d.render(w, r, code, "error", "Operational settings not applied", struct{ Title, Message, Retry string }{"Operational settings not applied", message, "/settings#operations"})
}
