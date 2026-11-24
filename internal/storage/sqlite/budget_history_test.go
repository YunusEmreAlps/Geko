package sqlite

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func historyCheckpoint(hour time.Time, session string, n int64) budget.Checkpoint {
	return budget.Checkpoint{Session: session, Start: hour, Since: hour, Until: hour.Add(30 * time.Minute), Spans: n, Bytes: n * 10, Services: []budget.Volume{{Namespace: "Shop", Service: "api", Spans: n, Bytes: n * 10}}}
}

func TestBudgetHistoryIdempotentRestartAndRetention(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "budget.db")
	hour := time.Now().UTC().Truncate(time.Hour)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	c := historyCheckpoint(hour, strings.Repeat("a", 32), 3)
	for range 2 {
		if err := db.SaveBudgetCheckpoints(ctx, hour, 7, []budget.Checkpoint{c}); err != nil {
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
	defer db.Close()
	var total int64
	_, err = db.ReadBudgetHistory(ctx, hour, "", func(saved budget.Checkpoint) error { total += saved.Spans; return nil })
	if err != nil || total != 3 {
		t.Fatalf("restarted total %d, %v", total, err)
	}
	newHour := hour.Add(10 * 24 * time.Hour)
	if err := db.SaveBudgetCheckpoints(ctx, newHour, 7, []budget.Checkpoint{historyCheckpoint(newHour, strings.Repeat("b", 32), 1)}); err != nil {
		t.Fatal(err)
	}
	total = 0
	if _, err := db.ReadBudgetHistory(ctx, hour, "", func(saved budget.Checkpoint) error { total += saved.Spans; return nil }); err != nil || total != 0 {
		t.Fatalf("retention total %d, %v", total, err)
	}
}

func TestBudgetHistoryBoundedAndCapacityDoesNotEvictTraces(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	hour := time.Now().UTC().Truncate(time.Hour)
	c := historyCheckpoint(hour, strings.Repeat("a", 32), 1)
	data, _ := json.Marshal(c)
	_, err := db.db.ExecContext(ctx, `WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<?) INSERT INTO budget_checkpoints SELECT printf('%032x',n),?,?,json_set(?,'$.session',printf('%032x',n)) FROM numbers`, maxBudgetCheckpoints, hour.UnixNano(), hour.UnixNano(), string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBudgetCheckpoints(ctx, hour, 7, []budget.Checkpoint{historyCheckpoint(hour, strings.Repeat("f", 32), 2)}); err != nil {
		t.Fatal(err)
	}
	var count int
	var discarded int64
	if err := db.db.QueryRow("SELECT count(*) FROM budget_checkpoints").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow("SELECT discarded_checkpoints FROM budget_history_meta").Scan(&discarded); err != nil {
		t.Fatal(err)
	}
	if count != maxBudgetCheckpoints || discarded != 1 {
		t.Fatalf("budget bound %d, discarded %d", count, discarded)
	}
	status, err := db.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db.maxBytes = status.MainBytes
	if err := db.SaveBudgetCheckpoints(ctx, hour, 7, []budget.Checkpoint{historyCheckpoint(hour, strings.Repeat("e", 32), 2)}); !errors.Is(err, storage.ErrCapacity) {
		t.Fatalf("capacity error %v", err)
	}
	after, err := db.Status(ctx)
	if err != nil || after.EvictedTraces != status.EvictedTraces || after.RejectedExports != status.RejectedExports {
		t.Fatalf("metadata altered ingest loss counters: %+v, %v", after, err)
	}
}

func TestBudgetCheckpointYieldsToBusyWriter(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	hour := time.Now().UTC().Truncate(time.Hour)
	connection, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = db.SaveBudgetCheckpoints(ctx, hour, 7, []budget.Checkpoint{historyCheckpoint(hour, strings.Repeat("a", 32), 1)})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("checkpoint did not yield: %v", err)
	}
	connection.Close()
	if err := db.WriteSpans(ctx, time.Now(), []telemetry.Span{testSpan(1, 1, "api", 200)}); err != nil {
		t.Fatalf("ingest did not recover: %v", err)
	}
}
