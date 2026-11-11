package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"geko/internal/investigation"
	"geko/internal/telemetry"
)

func TestHistoricalAssignmentResumesAcrossRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "assign.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Now().Add(-time.Hour)
	spans := make([]telemetry.Span, 1100)
	for i := range spans {
		spans[i] = retentionSpan(0, "legacy")
		binary.BigEndian.PutUint64(spans[i].TraceID[8:], uint64(i+1))
	}
	if err := db.WriteSpans(ctx, at, spans); err != nil {
		t.Fatal(err)
	}
	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	changed, done, err := db.assignTraceBatch(ctx, assignmentScope{from.UnixNano(), to.UnixNano(), "fixture", "Reporting"})
	if err != nil || done || changed != assignmentBatchSize {
		t.Fatalf("first batch = %d, %t, %v", changed, done, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssignTraceApplication(ctx, from, to, "fixture", "Storefront"); !errors.Is(err, investigation.ErrAssignmentInProgress) {
		t.Fatalf("conflicting assignment: %v", err)
	}
	if changed, err := db.AssignTraceApplication(ctx, from, to, "fixture", "Reporting"); err != nil || changed != int64(len(spans)) {
		t.Fatalf("resume = %d, %v", changed, err)
	}
	if changed, err := db.AssignTraceApplication(ctx, from, to, "fixture", "Reporting"); err != nil || changed != 0 {
		t.Fatalf("idempotent retry = %d, %v", changed, err)
	}
	if changed, err := db.AssignTraceApplication(ctx, from, to, "fixture", ""); err != nil || changed != int64(len(spans)) {
		t.Fatalf("clear = %d, %v", changed, err)
	}
}

func TestReadersCanInspectWhileWriterIsBusy(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id := telemetry.TraceID{1}
	if _, err := tx.ExecContext(ctx, observeTrace, id[:], 100, 100); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if found, err := db.HasTraces(readCtx); err != nil || found {
		t.Fatalf("reader should see the previous snapshot: found=%t, %v", found, err)
	}
	if _, err := Inspect(readCtx, db.path); err != nil {
		t.Fatalf("read-only doctor waited on the writer: %v", err)
	}
}
