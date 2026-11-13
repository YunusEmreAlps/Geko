package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func (d *dashboard) downloadTrace(w http.ResponseWriter, r *http.Request) {
	id, err := api.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid trace ID", err.Error())
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		body, err := otlp.EncodeTraceJSON(id, d.settings.Redaction == "strict", func(visit func(investigation.Span) error) error {
			return d.store.VisitTraceSpans(ctx, id, visit)
		})
		switch {
		case errors.Is(err, investigation.ErrTraceNotFound):
			d.problem(w, r, http.StatusNotFound, "Trace not found", "The trace may have expired or been evicted.")
		case errors.Is(err, otlp.ErrTransferLimit):
			d.problem(w, r, http.StatusRequestEntityTooLarge, "Trace exceeds the download limit", err.Error())
		case errors.Is(err, otlp.ErrInvalidTransfer):
			d.problem(w, r, http.StatusUnprocessableEntity, "Trace cannot be exported", err.Error())
		case err != nil:
			d.unavailable(w, r, ctx)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"trace-%x.otlp.json\"", id[:]))
			_, _ = w.Write(body)
		}
	})
}

func (d *dashboard) traceStored(ctx context.Context, id telemetry.TraceID) (bool, error) {
	_, _, err := d.store.Trace(ctx, id, nil, 1)
	if errors.Is(err, investigation.ErrTraceNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (d *dashboard) restoreTrace(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, otlp.TransferBytes+(64<<10))
	defer r.Body.Close()
	reader, err := r.MultipartReader()
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid upload", "Choose an OTLP JSON file from All traces.")
		return
	}
	file, err := reader.NextPart()
	if err != nil || file.FormName() != "trace_file" || file.FileName() == "" {
		d.problem(w, r, http.StatusBadRequest, "Missing trace file", "Choose an OTLP JSON file from All traces.")
		return
	}
	id, err := d.settings.RestoreTrace(r.Context(), file, d.traceStored)
	switch {
	case errors.Is(err, otlp.ErrTraceExists):
		d.problem(w, r, http.StatusConflict, "Trace already stored", "Open /traces/"+hex.EncodeToString(id[:])+" to inspect it, or restore the file into another Geko database.")
	case errors.Is(err, otlp.ErrTransferLimit):
		d.problem(w, r, http.StatusRequestEntityTooLarge, "Upload exceeds the limit", err.Error())
	case errors.Is(err, otlp.ErrInvalidTransfer):
		d.problem(w, r, http.StatusBadRequest, "Invalid trace file", err.Error())
	case errors.Is(err, storage.ErrCapacity):
		w.Header().Set("Retry-After", "30")
		d.problem(w, r, http.StatusServiceUnavailable, "Storage capacity reached", "The trace could not be restored within the storage limit.")
	case errors.Is(err, otlp.ErrTransferBusy):
		w.Header().Set("Retry-After", "1")
		d.problem(w, r, http.StatusServiceUnavailable, "Receiver is busy", "Try uploading the file again.")
	case err != nil:
		d.unavailable(w, r, r.Context())
	default:
		http.Redirect(w, r, "/traces/"+hex.EncodeToString(id[:]), http.StatusSeeOther)
	}
}
