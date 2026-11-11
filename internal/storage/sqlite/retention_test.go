package sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"geko/internal/investigation"
	"geko/internal/storage"
	"geko/internal/telemetry"
)

func TestPruneExpiresWholeTracesAcrossRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "retention.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	now := time.Now()
	cutoff := now.Add(-7 * 24 * time.Hour)
	expired := retentionSpan(1, "expired")
	active := retentionSpan(2, "active")
	boundary := retentionSpan(3, "boundary")
	for _, batch := range []struct {
		at    time.Time
		spans []telemetry.Span
	}{
		{cutoff.Add(-time.Nanosecond), []telemetry.Span{expired, active}},
		{cutoff, []telemetry.Span{boundary}},
		{now, []telemetry.Span{active}},
	} {
		if err := db.WriteSpans(ctx, batch.at, batch.spans); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Prune(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Trace(ctx, expired.TraceID, nil, 10); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("expired trace lookup: %v", err)
	}
	if _, err := db.Span(ctx, expired.TraceID, expired.ID); !errors.Is(err, investigation.ErrSpanNotFound) {
		t.Fatalf("expired span lookup: %v", err)
	}
	page, err := db.Failures(ctx, investigation.FailureFilter{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("remaining failure evidence = %+v, %v", page, err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("WAL after prune = %d bytes", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestStorageLimitRejectsBatchAndReusesExpiredSpace(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "quota.db")
	const limit = 1 << 20
	db, err := OpenWithPolicy(ctx, path, limit, storage.Reject)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	old := retentionSpan(1, strings.Repeat("x", 750<<10))
	if err := db.WriteSpans(ctx, now.Add(-time.Hour), []telemetry.Span{old}); err != nil {
		t.Fatal(err)
	}
	small := retentionSpan(2, "must roll back")
	large := retentionSpan(3, strings.Repeat("y", 600<<10))
	err = db.WriteSpans(ctx, now, []telemetry.Span{small, large})
	if !errors.Is(err, storage.ErrCapacity) {
		t.Fatalf("write above limit = %v, want capacity error", err)
	}
	status, err := db.Status(ctx)
	if err != nil || status.RejectedExports != 1 || status.EvictedTraces != 0 {
		t.Fatalf("reject counters = %+v, %v", status, err)
	}
	if _, _, err := db.Trace(ctx, small.TraceID, nil, 10); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("partial write survived quota rejection: %v", err)
	}
	if err := db.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteSpans(ctx, now, []telemetry.Span{large}); err != nil {
		t.Fatalf("write after expiry: %v", err)
	}
	if err := db.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > limit {
		t.Fatalf("main database grew to %d bytes above the %d-byte limit", info.Size(), limit)
	}
	if err := db.Prune(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	before, err := db.Status(ctx)
	if err != nil || before.FreeBytes == 0 {
		t.Fatalf("expected reusable space before compaction: %+v, %v", before, err)
	}
	if err := db.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := db.Status(ctx)
	if err != nil || after.MainBytes >= before.MainBytes || after.FreeBytes != 0 || after.RejectedExports != 1 {
		t.Fatalf("compaction before=%+v after=%+v, %v", before, after, err)
	}
}

func TestEvictionCommitsWithExportAndCountersSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "eviction.db")
	db, err := OpenWithLimit(ctx, path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	old := retentionSpan(1, strings.Repeat("x", 750<<10))
	active := retentionSpan(2, "active")
	if err := db.WriteSpans(ctx, now.Add(-time.Hour), []telemetry.Span{old, active}); err != nil {
		t.Fatal(err)
	}
	oversized := retentionSpan(3, strings.Repeat("z", 1200<<10))
	if err := db.WriteSpans(ctx, now, []telemetry.Span{oversized}); !errors.Is(err, storage.ErrCapacity) {
		t.Fatalf("oversized export: %v", err)
	}
	if _, _, err := db.Trace(ctx, old.TraceID, nil, 10); err != nil {
		t.Fatalf("failed export evicted old trace: %v", err)
	}
	continued := active
	continued.ID = telemetry.SpanID{2}
	incoming := retentionSpan(4, strings.Repeat("y", 600<<10))
	if err := db.WriteSpans(ctx, now, []telemetry.Span{continued, incoming}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenWithLimit(ctx, path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	status, err := db.Status(ctx)
	if err != nil || status.EvictedTraces != 1 || status.RejectedExports != 1 {
		t.Fatalf("reopened counters = %+v, %v", status, err)
	}
	if _, _, err := db.Trace(ctx, old.TraceID, nil, 10); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("evicted trace lookup: %v", err)
	}
	if _, page, err := db.Trace(ctx, active.TraceID, nil, 10); err != nil || len(page.Items) != 2 {
		t.Fatalf("continued trace lost a span: %+v, %v", page, err)
	}
	if _, _, err := db.Trace(ctx, incoming.TraceID, nil, 10); err != nil {
		t.Fatalf("new trace lookup: %v", err)
	}
}

func retentionSpan(id byte, name string) telemetry.Span {
	return telemetry.Span{
		TraceID: telemetry.TraceID{id}, ID: telemetry.SpanID{1}, Name: name,
		ServiceName: "fixture", StatusCode: telemetry.StatusError,
		StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0),
	}
}
