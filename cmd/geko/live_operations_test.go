package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestLiveMaintenanceReadsPublishedPolicy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "geko.yaml")
	data := []byte("retention_days: 7\ntelemetry_budget: {enabled: true}\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.NewRuntime(config.Config{RetentionDays: 7, MaxDBMiB: 256, IngestSlots: 4, OnFull: storage.EvictOldest, Redaction: "best_effort", TelemetryBudget: budget.Config{Enabled: true}, Source: config.Source{FilePath: file, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data))}})
	db, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	id := telemetry.TraceID{1}
	if err := db.WriteSpans(t.Context(), now.Add(-3*24*time.Hour), []telemetry.Span{{TraceID: id, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "old", StartTime: now, EndTime: now.Add(time.Millisecond)}}); err != nil {
		t.Fatal(err)
	}
	pruneLive(t.Context(), db, runtime)
	if _, err := db.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{}); err != nil {
		t.Fatal("old retention deleted trace", err)
	}
	next := runtime.Operations()
	next.RetentionDays = 1
	next.BudgetHistoryDays = 7
	if _, err := runtime.UpdateOperations(t.Context(), runtime.Snapshot().Revision, next, true); err != nil {
		t.Fatal(err)
	}
	pruneLive(t.Context(), db, runtime)
	if _, err := db.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{}); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatal("new retention not used", err)
	}
	tracker := budget.New(budget.Config{Enabled: true}, now).WithPolicySource(runtime.BudgetPolicy)
	checkpointBudget(t.Context(), db, tracker)
	var found bool
	if _, err := db.ReadBudgetHistory(t.Context(), now.UTC().Truncate(time.Hour), "", func(budget.Checkpoint) error { found = true; return nil }); err != nil || !found {
		t.Fatal("live history was not checkpointed", err)
	}
	next.BudgetHistoryDays = 0
	if _, err := runtime.UpdateOperations(t.Context(), runtime.Snapshot().Revision, next, true); err != nil {
		t.Fatal(err)
	}
	before := tracker.Snapshot(now, false).CheckpointedAt
	checkpointBudget(t.Context(), db, tracker)
	if tracker.Snapshot(now, false).CheckpointedAt != before {
		t.Fatal("disabled history still checkpointed")
	}
}
