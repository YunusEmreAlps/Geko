package sqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestPinnedTraceOutlivesRetention(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "pins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	cutoff := now.Add(-7 * 24 * time.Hour)
	pinned, expired := retentionSpan(1, "pinned"), retentionSpan(2, "expired")
	if err := db.WriteSpans(ctx, cutoff.Add(-time.Hour), []telemetry.Span{pinned, expired}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTracePin(ctx, telemetry.TraceID{9}, true, "qa", now); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("pin missing trace: %v", err)
	}
	if err := db.SetTracePin(ctx, pinned.TraceID, true, "qa", now); err != nil {
		t.Fatal(err)
	}
	// Pinning again keeps who pinned it first.
	if err := db.SetTracePin(ctx, pinned.TraceID, true, "other", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.Prune(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	summary, _, err := db.Trace(ctx, pinned.TraceID, nil, 1)
	if err != nil || summary.Pin == nil || summary.Pin.PinnedBy != "qa" || !summary.Pin.PinnedAt.Equal(time.Unix(0, now.UnixNano()).UTC()) {
		t.Fatalf("pinned trace after prune = %+v, %v", summary.Pin, err)
	}
	if _, _, err := db.Trace(ctx, expired.TraceID, nil, 1); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("unpinned trace lookup: %v", err)
	}
	page, err := db.Traces(ctx, investigation.FailureFilter{Pinned: true, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Pin == nil {
		t.Fatalf("pinned traces = %+v, %v", page.Items, err)
	}
	if status, err := db.Status(ctx); err != nil || status.PinnedTraces != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}

	if err := db.SetTracePin(ctx, pinned.TraceID, false, "", now); err != nil {
		t.Fatal(err)
	}
	if page, err := db.Traces(ctx, investigation.FailureFilter{Pinned: true, Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("pinned traces after unpin = %+v, %v", page.Items, err)
	}
	if err := db.Prune(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Trace(ctx, pinned.TraceID, nil, 1); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("unpinned trace survived prune: %v", err)
	}
}

func TestEvictionSkipsPinnedTraces(t *testing.T) {
	for _, policy := range []storage.FullPolicy{storage.EvictOldest, storage.EvictFair} {
		t.Run(string(policy), func(t *testing.T) {
			ctx := t.Context()
			db, err := OpenWithPolicy(ctx, filepath.Join(t.TempDir(), "pinned-eviction.db"), 1<<20, policy)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now()
			oldest := retentionSpan(1, strings.Repeat("x", 300<<10))
			older := retentionSpan(2, strings.Repeat("y", 300<<10))
			if err := db.WriteSpans(ctx, now.Add(-2*time.Hour), []telemetry.Span{oldest}); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteSpans(ctx, now.Add(-time.Hour), []telemetry.Span{older}); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTracePin(ctx, oldest.TraceID, true, "", now); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteSpans(ctx, now, []telemetry.Span{retentionSpan(3, strings.Repeat("z", 400<<10))}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.Trace(ctx, oldest.TraceID, nil, 1); err != nil {
				t.Fatalf("pinned trace evicted: %v", err)
			}
			if _, _, err := db.Trace(ctx, older.TraceID, nil, 1); !errors.Is(err, investigation.ErrTraceNotFound) {
				t.Fatalf("unpinned trace kept: %v", err)
			}
		})
	}
}
