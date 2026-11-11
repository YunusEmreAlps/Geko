package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigrateAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data # %", "geko.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	status, err := db.Check(t.Context())
	if err != nil || status.SchemaVersion != 5 || status.SQLiteVersion == "" || status.Bytes <= 0 {
		t.Fatalf("Check() = %+v, %v", status, err)
	}

	var mode string
	if err := db.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode = %q, %v", mode, err)
	}
	if _, err := db.db.Exec("INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns) VALUES (randomblob(16), 100, 100)"); err != nil {
		t.Fatal(err)
	}
	// Preserve a child whose parent has yet to arrive, and preserve its timestamps as recorded.
	_, err = db.db.Exec(`INSERT INTO spans
		(trace_id, span_id, parent_span_id, service_name, name, kind, start_ns, end_ns, status_code)
		SELECT trace_id, randomblob(8), randomblob(8), 'api', 'request', 2, 100, 90, 0 FROM traces`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	var count int
	if err := reopened.db.QueryRow("SELECT count(*) FROM spans WHERE end_ns = 90").Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted spans = %d, %v", count, err)
	}
	if _, err := reopened.db.Exec("DELETE FROM traces"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 0 {
		t.Fatalf("spans after trace deletion = %d, %v", count, err)
	}
}

func TestRejectUnrelatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err == nil {
		store.Close()
		t.Fatal("Open() accepted an unrelated database")
	}
	if !strings.Contains(err.Error(), "belongs to another application") {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("unrelated database journal mode = %q, %v", mode, err)
	}
}

func TestRejectNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err == nil {
		store.Close()
		t.Fatal("Open() accepted a newer schema")
	}
	if !strings.Contains(err.Error(), "unsupported schema version 999") {
		t.Fatal(err)
	}
}

func TestRejectCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("corrupt SQLite file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err == nil {
		store.Close()
		t.Fatal("Open() accepted a corrupt database")
	}
}
