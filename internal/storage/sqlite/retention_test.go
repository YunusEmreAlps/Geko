package sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
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

func TestLoweredCapacityEvictsWithoutGrowingDatabase(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "lowered-limit.db")
	db, err := OpenWithLimit(ctx, path, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := retentionSpan(1, strings.Repeat("x", 750<<10))
	err = db.WriteSpans(ctx, now.Add(-time.Hour), []telemetry.Span{old})
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}

	const loweredLimit = 256 << 10
	db, err = OpenWithLimit(ctx, path, loweredLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Status(ctx)
	if err != nil || before.MainBytes <= loweredLimit {
		t.Fatalf("fixture must exceed the lowered limit: %+v, %v", before, err)
	}
	incoming := retentionSpan(2, strings.Repeat("y", 600<<10))
	if err := db.WriteSpans(ctx, now, []telemetry.Span{incoming}); err != nil {
		t.Fatalf("export with lowered limit: %v", err)
	}
	if _, _, err := db.Trace(ctx, old.TraceID, nil, 10); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("old trace should be evicted: %v", err)
	}
	if _, _, err := db.Trace(ctx, incoming.TraceID, nil, 10); err != nil {
		t.Fatalf("incoming trace lookup: %v", err)
	}
	var pageBytes int64
	if err := db.read.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&pageBytes); err != nil || pageBytes > before.MainBytes {
		t.Fatalf("database grew beyond its existing size: %d > %d, %v", pageBytes, before.MainBytes, err)
	}
	after, err := db.Status(ctx)
	if err != nil || after.EvictedTraces != 1 || after.RejectedExports != 0 {
		t.Fatalf("capacity counters: %+v, %v", after, err)
	}
}

func TestPruneSkipsSmallJournalWithoutExpiredTraces(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	now := time.Now()
	if err := db.WriteSpans(ctx, now, []telemetry.Span{retentionSpan(1, "active")}); err != nil {
		t.Fatal(err)
	}
	small, err := db.journalSize()
	if err != nil || small == 0 || small >= maintenanceWALThreshold {
		t.Fatalf("expected a small journal: %d, %v", small, err)
	}
	if err := db.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if size, err := db.journalSize(); err != nil || size != small {
		t.Fatalf("no-op retention changed small journal: %d, %v", size, err)
	}
	large := retentionSpan(2, strings.Repeat("x", maintenanceWALThreshold))
	if err := db.WriteSpans(ctx, now, []telemetry.Span{large}); err != nil {
		t.Fatal(err)
	}
	if size, err := db.journalSize(); err != nil || size < maintenanceWALThreshold {
		t.Fatalf("expected a large journal: %d, %v", size, err)
	}
	if err := db.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if size, err := db.journalSize(); err != nil || size != 0 {
		t.Fatalf("maintenance did not truncate the large journal: %d, %v", size, err)
	}
}

func retentionSpan(id byte, name string) telemetry.Span {
	return telemetry.Span{
		TraceID: telemetry.TraceID{id}, ID: telemetry.SpanID{1}, Name: name,
		ServiceName: "fixture", StatusCode: telemetry.StatusError,
		StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0),
	}
}

func TestFairEvictionKeepsQuietServices(t *testing.T) {
	for _, tc := range []struct {
		policy    storage.FullPolicy
		quietKept bool
	}{{storage.EvictOldest, false}, {storage.EvictFair, true}} {
		t.Run(string(tc.policy), func(t *testing.T) {
			ctx := t.Context()
			db, err := OpenWithPolicy(ctx, filepath.Join(t.TempDir(), "fair.db"), 1<<20, tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now()
			span := func(id byte, service string, size int) telemetry.Span {
				s := retentionSpan(id, strings.Repeat("x", size))
				s.ServiceName = service
				return s
			}
			quiet := span(1, "quiet", 100<<10)
			if err := db.WriteSpans(ctx, now.Add(-2*time.Hour), []telemetry.Span{quiet}); err != nil {
				t.Fatal(err)
			}
			for i := range 6 {
				if err := db.WriteSpans(ctx, now.Add(-time.Hour+time.Duration(i)*time.Minute), []telemetry.Span{span(byte(10+i), "noisy", 100<<10)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.WriteSpans(ctx, now, []telemetry.Span{span(30, "noisy", 300<<10)}); err != nil {
				t.Fatal(err)
			}
			status, err := db.Status(ctx)
			if err != nil || status.EvictedTraces == 0 {
				t.Fatalf("status = %+v, %v", status, err)
			}
			_, _, err = db.Trace(ctx, quiet.TraceID, nil, 1)
			if kept := err == nil; kept != tc.quietKept {
				t.Fatalf("quiet trace kept = %v, want %v (%v)", kept, tc.quietKept, err)
			}
			if _, _, err := db.Trace(ctx, telemetry.TraceID{30}, nil, 1); err != nil {
				t.Fatalf("incoming trace: %v", err)
			}
		})
	}
}

func TestFairEvictionFallsBackToOldest(t *testing.T) {
	ctx := t.Context()
	db, err := OpenWithPolicy(ctx, filepath.Join(t.TempDir(), "fallback.db"), 1<<20, storage.EvictFair)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	old := retentionSpan(1, strings.Repeat("x", 300<<10))
	old.ServiceName = "small"
	if err := db.WriteSpans(ctx, now.Add(-time.Hour), []telemetry.Span{old}); err != nil {
		t.Fatal(err)
	}
	large := func(id byte) telemetry.Span {
		s := retentionSpan(20, strings.Repeat("y", 150<<10))
		s.ID, s.ServiceName = telemetry.SpanID{id}, "large"
		return s
	}
	if err := db.WriteSpans(ctx, now.Add(-30*time.Minute), []telemetry.Span{large(1), large(2), large(3)}); err != nil {
		t.Fatal(err)
	}
	// "large" holds the most spans, but its only trace continues in this
	// export and is protected, so the oldest other trace is evicted instead.
	continued := large(4)
	continued.Name = strings.Repeat("z", 300<<10)
	if err := db.WriteSpans(ctx, now, []telemetry.Span{continued}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Trace(ctx, old.TraceID, nil, 1); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("old trace lookup: %v", err)
	}
	if _, page, err := db.Trace(ctx, telemetry.TraceID{20}, nil, 10); err != nil || len(page.Items) != 4 {
		t.Fatalf("protected trace = %d spans, %v", len(page.Items), err)
	}
}
