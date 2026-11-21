package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

const (
	bundleFormat = "geko.failure-bundle/v1"
	// bundleTraces bounds the traces a bundle carries, newest first.
	bundleTraces = 10
	bundleBytes  = bundleTraces*otlp.TransferBytes + 64<<10
)

// failureBundle carries one error group from one Geko to another: its
// summary and note, and its newest traces as OTLP JSON.
type failureBundle struct {
	Format     string                     `json:"format"`
	ExportedAt time.Time                  `json:"exported_at"`
	Version    string                     `json:"geko_version,omitempty"`
	Group      investigation.FailureGroup `json:"group"`
	Traces     []json.RawMessage          `json:"traces"`
}

func (d *dashboard) downloadBundle(w http.ResponseWriter, r *http.Request) {
	key, err := investigation.DecodeGroupKey(r.URL.Query().Get("group"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid group", "Open the group from Error groups and download it again.")
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		groups, err := d.store.FailureGroups(ctx, investigation.FailureFilter{Limit: 1, GroupKey: key, GroupState: investigation.GroupAll}, nil)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		if len(groups.Items) == 0 {
			d.problem(w, r, http.StatusNotFound, "Group not found", "Its traces may have expired or been evicted.")
			return
		}
		traces, err := d.store.Failures(ctx, investigation.FailureFilter{Limit: bundleTraces, GroupKey: key})
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		bundle := failureBundle{Format: bundleFormat, ExportedAt: time.Now().UTC(), Version: d.settings.Version, Group: groups.Items[0], Traces: []json.RawMessage{}}
		for _, trace := range traces.Items {
			traceID, err := api.ParseTraceID(trace.TraceID)
			if err != nil {
				continue
			}
			body, err := otlp.EncodeTraceJSON(traceID, d.settings.Redaction == "strict", func(visit func(investigation.Span) error) error {
				return d.store.VisitTraceSpans(ctx, traceID, visit)
			})
			// A trace beyond the transfer limit stays out; the rest still travel.
			if errors.Is(err, otlp.ErrTransferLimit) || errors.Is(err, otlp.ErrInvalidTransfer) || errors.Is(err, investigation.ErrTraceNotFound) {
				continue
			}
			if err != nil {
				d.unavailable(w, r, ctx)
				return
			}
			bundle.Traces = append(bundle.Traces, body)
		}
		data, err := json.Marshal(bundle)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"geko-bundle-%s.json\"", groups.Items[0].TraceID[:12]))
		_, _ = w.Write(data)
	})
}

func (d *dashboard) importBundle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, bundleBytes+(64<<10))
	defer r.Body.Close()
	reader, err := r.MultipartReader()
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid upload", "Choose a failure bundle downloaded from an error group.")
		return
	}
	file, err := reader.NextPart()
	if err != nil || file.FormName() != "bundle_file" || file.FileName() == "" {
		d.problem(w, r, http.StatusBadRequest, "Missing bundle", "Choose a failure bundle downloaded from an error group.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, bundleBytes+1))
	var bundle failureBundle
	switch {
	case err != nil || len(data) > bundleBytes:
		d.problem(w, r, http.StatusRequestEntityTooLarge, "Bundle exceeds the limit", fmt.Sprintf("A bundle holds at most %d traces of up to 4 MiB each.", bundleTraces))
		return
	case json.Unmarshal(data, &bundle) != nil || bundle.Format != bundleFormat || len(bundle.Traces) == 0 || len(bundle.Traces) > bundleTraces:
		d.problem(w, r, http.StatusBadRequest, "Invalid bundle", "Choose a "+bundleFormat+" file with between 1 and 10 traces.")
		return
	}
	restored, present := 0, 0
	var first telemetry.TraceID
	for i, trace := range bundle.Traces {
		id, err := d.settings.RestoreTrace(r.Context(), bytes.NewReader(trace), d.traceStored)
		switch {
		case errors.Is(err, otlp.ErrTraceExists):
			present++
		case errors.Is(err, otlp.ErrTransferLimit), errors.Is(err, otlp.ErrInvalidTransfer):
			d.problem(w, r, http.StatusBadRequest, "Invalid trace in the bundle", fmt.Sprintf("Trace %d of %d: %v. %d traces were restored before it.", i+1, len(bundle.Traces), err, restored))
			return
		case errors.Is(err, storage.ErrCapacity):
			w.Header().Set("Retry-After", "30")
			d.problem(w, r, http.StatusServiceUnavailable, "Storage capacity reached", fmt.Sprintf("%d traces were restored before the limit.", restored))
			return
		case errors.Is(err, otlp.ErrTransferBusy):
			w.Header().Set("Retry-After", "1")
			d.problem(w, r, http.StatusServiceUnavailable, "Receiver is busy", "Import the bundle again; traces already restored are skipped.")
			return
		case err != nil:
			d.unavailable(w, r, r.Context())
			return
		default:
			restored++
		}
		if i == 0 {
			first = id
		}
	}
	// The group key depends on this instance's application mappings, so find
	// the group the first trace landed in rather than trusting the source's.
	groups, err := d.store.FailureGroups(r.Context(), investigation.FailureFilter{Limit: 1, Search: hex.EncodeToString(first[:]), GroupState: investigation.GroupAll}, nil)
	if err != nil {
		d.unavailable(w, r, r.Context())
		return
	}
	target := "/failures?view=groups&state=all"
	if len(groups.Items) > 0 {
		group := groups.Items[0]
		target = "/failures?" + url.Values{"group": {investigation.EncodeGroupKey(group.Key)}}.Encode()
		source := bundle.Group
		if group.Note == "" && group.IssueURL == "" && (source.Note != "" || source.IssueURL != "") &&
			investigation.ValidateGroupNote(source.Note, source.IssueURL) == nil {
			if err := d.store.SetGroupNote(r.Context(), group.Key, source.Note, source.IssueURL, d.changedBy(r), time.Now()); err != nil {
				d.unavailable(w, r, r.Context())
				return
			}
		}
	}
	d.settings.Audit.Record(r.Context(), r, audit.Event{Action: "group.import", Target: hex.EncodeToString(first[:]),
		Detail: map[string]string{"restored": fmt.Sprint(restored), "already_stored": fmt.Sprint(present)}})
	http.Redirect(w, r, target, http.StatusSeeOther)
}
