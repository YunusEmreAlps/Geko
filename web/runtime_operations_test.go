package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestOperationsPreviewAndApply(t *testing.T) {
	file := filepath.Join(t.TempDir(), "geko.yaml")
	data := []byte("retention_days: 7\ntelemetry_budget: {enabled: true}\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.NewRuntime(config.Config{RetentionDays: 7, MaxDBMiB: 256, IngestSlots: 4, OnFull: storage.EvictOldest, Redaction: "best_effort", TelemetryBudget: budget.Config{Enabled: true}, Source: config.Source{FilePath: file, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data))}})
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.WriteSpans(t.Context(), now.Add(-10*24*time.Hour), []telemetry.Span{{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, StatusCode: telemetry.StatusError, Events: []telemetry.Event{{Name: "exception", Time: now, Attributes: telemetry.Attributes{"exception.type": "Timeout", "exception.message": "old-private-message-marker"}}}, Name: "old", ServiceName: "api", StartTime: now, EndTime: now.Add(time.Millisecond)}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	next := runtime.Operations()
	next.RetentionDays = 1
	next.MaxDBMiB = 64
	next.Redaction = "strict"
	input := map[string]any{"revision": runtime.Snapshot().Revision, "policy": next}
	do := func(path string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://127.0.0.1"+path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}
	preview := do("/api/v1/settings/operations/preview")
	if preview.Code != 200 {
		t.Fatalf("preview %d %s", preview.Code, preview.Body)
	}
	var observed struct {
		RetentionCandidates *int64 `json:"retention_candidates"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &observed); err != nil || observed.RetentionCandidates == nil || *observed.RetentionCandidates != 1 {
		t.Fatalf("preview count: %s", preview.Body)
	}
	if runtime.Operations().RetentionDays != 7 {
		t.Fatal("preview applied")
	}
	if w := do("/api/v1/settings/operations"); w.Code != 400 {
		t.Fatalf("unconfirmed: %d", w.Code)
	}
	input["confirmed"] = true
	if w := do("/api/v1/settings/operations"); w.Code != 200 || runtime.Operations().RetentionDays != 1 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		return w
	}
	download := get("/traces/01000000000000000000000000000000/otlp.json")
	if download.Code != 200 || strings.Contains(download.Body.String(), "old-private-message-marker") {
		t.Error("live strict download leaked old message")
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 10}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatal(err)
	}
	bundle := get("/groups/bundle?group=" + url.QueryEscape(investigation.EncodeGroupKey(groups.Items[0].Key)))
	if bundle.Code != 200 || strings.Contains(bundle.Body.String(), "old-private-message-marker") {
		t.Error("live strict bundle leaked old message")
	}
	metrics := get("/metrics")
	if !strings.Contains(metrics.Body.String(), "geko_storage_limit_bytes 67108864") {
		t.Error("metrics retained old capacity")
	}
	if w := do("/api/v1/settings/operations"); w.Code != 409 {
		t.Fatalf("stale: %d", w.Code)
	}
}
