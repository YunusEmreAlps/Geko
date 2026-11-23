package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestUpgradeMeasurement upgrades a copy of an older Geko database and records
// the peak main and WAL file sizes, an interrupted upgrade, and an upgrade and
// compaction while another reader holds a transaction. It runs only with
// GEKO_UPGRADE_FIXTURE set to a database written by an older release, which
// it only copies; GEKO_UPGRADE_OUT receives the measurements as JSON.
func TestUpgradeMeasurement(t *testing.T) {
	fixture := os.Getenv("GEKO_UPGRADE_FIXTURE")
	if fixture == "" {
		t.Skip("set GEKO_UPGRADE_FIXTURE to an older Geko database")
	}
	ctx := t.Context()
	dir := t.TempDir()
	copyFixture := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		source, err := os.Open(fixture)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		target, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(target, source); err != nil {
			t.Fatal(err)
		}
		if err := target.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	type facts struct {
		Version       int    `json:"schema_version"`
		Traces, Spans int64  `json:"-"`
		Integrity     string `json:"-"`
	}
	inspect := func(path string) facts {
		t.Helper()
		// A cancelled upgrade releases its connection asynchronously.
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var f facts
		if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&f.Version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM traces), (SELECT count(*) FROM spans)").Scan(&f.Traces, &f.Spans); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&f.Integrity); err != nil {
			t.Fatal(err)
		}
		return f
	}
	size := func(path string) int64 {
		info, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return info.Size()
	}
	// upgrade opens path, sampling file sizes until it returns.
	upgrade := func(ctx context.Context, path string) (*Store, time.Duration, int64, int64, error) {
		var peakMain, peakWAL atomic.Int64
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				if main := size(path); main > peakMain.Load() {
					peakMain.Store(main)
				}
				if wal := size(path + "-wal"); wal > peakWAL.Load() {
					peakWAL.Store(wal)
				}
				select {
				case <-done:
					return
				case <-ticker.C:
				}
			}
		})
		started := time.Now()
		store, err := Open(ctx, path)
		elapsed := time.Since(started)
		close(done)
		wg.Wait()
		return store, elapsed, peakMain.Load(), peakWAL.Load(), err
	}

	full := copyFixture("full.db")
	before := inspect(full)
	if before.Version >= len(migrations) || before.Integrity != "ok" {
		t.Fatalf("fixture is schema %d (%s); it must be older than %d", before.Version, before.Integrity, len(migrations))
	}
	result := map[string]any{
		"from_schema": before.Version, "to_schema": len(migrations), "traces": before.Traces, "spans": before.Spans,
		"fixture_bytes": size(fixture),
	}

	store, elapsed, peakMain, peakWAL, err := upgrade(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after := inspect(full)
	if after.Version != len(migrations) || after.Traces != before.Traces || after.Spans != before.Spans || after.Integrity != "ok" {
		t.Fatalf("upgrade changed data: before %+v after %+v", before, after)
	}
	result["upgrade"] = map[string]any{
		"seconds": elapsed.Seconds(), "peak_main_bytes": peakMain, "peak_wal_bytes": peakWAL,
		"peak_total_bytes": peakMain + peakWAL, "final_main_bytes": size(full), "final_free_bytes": status.FreeBytes,
		"peak_headroom_ratio": float64(peakMain+peakWAL) / float64(size(fixture)),
	}

	// An interrupted upgrade, like a stopped process, rolls back completely.
	interrupted := copyFixture("interrupted.db")
	cancelled, cancel := context.WithTimeout(ctx, elapsed/2)
	store, partial, _, _, err := upgrade(cancelled, interrupted)
	cancel()
	if err == nil {
		store.Close()
		t.Fatalf("the upgrade finished in %v despite cancellation at %v; use a larger fixture", partial, elapsed/2)
	}
	rolledBack := inspect(interrupted)
	if rolledBack.Version != before.Version || rolledBack.Traces != before.Traces || rolledBack.Spans != before.Spans || rolledBack.Integrity != "ok" {
		t.Fatalf("interrupted upgrade left %+v, want %+v", rolledBack, before)
	}
	store, retried, _, _, err := upgrade(ctx, interrupted)
	if err != nil {
		t.Fatalf("retry after interruption: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	result["interrupted"] = map[string]any{
		"cancelled_after_seconds": partial.Seconds(), "schema_after_cancel": rolledBack.Version,
		"data_unchanged": true, "retry_seconds": retried.Seconds(), "retry_schema": inspect(interrupted).Version,
	}

	// Another reader, such as a second geko process or a backup, holding a
	// read transaction during the upgrade and during compaction.
	shared := copyFixture("reader.db")
	reader, err := sql.Open("sqlite", "file:"+filepath.ToSlash(shared)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	conn, err := reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM traces").Scan(&count); err != nil {
		t.Fatal(err)
	}
	store, withReader, _, readerWAL, upgradeErr := upgrade(ctx, shared)
	readerResult := map[string]any{"upgrade_seconds": withReader.Seconds(), "peak_wal_bytes": readerWAL}
	if upgradeErr != nil {
		readerResult["upgrade_error"] = upgradeErr.Error()
	} else {
		mainBefore := size(shared)
		compactErr := store.Compact(ctx)
		readerResult["compact_with_reader_ok"] = compactErr == nil
		if compactErr != nil {
			readerResult["compact_with_reader_error"] = compactErr.Error()
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		reader.Close()
		compactErr = store.Compact(ctx)
		readerResult["compact_after_reader_closed_ok"] = compactErr == nil
		readerResult["compacted_from_bytes"], readerResult["compacted_to_bytes"] = mainBefore, size(shared)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	result["concurrent_reader"] = readerResult

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", data)
	if out := os.Getenv("GEKO_UPGRADE_OUT"); out != "" {
		if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
