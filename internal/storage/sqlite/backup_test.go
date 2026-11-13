package sqlite

import (
	"path/filepath"
	"testing"
)

func TestBackupWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geko.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec("INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns) VALUES (randomblob(16), 100, 100)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	target := filepath.Join(dir, "backup.db")
	if err := reader.Backup(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := reader.Backup(t.Context(), target); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
	if err := reader.Backup(t.Context(), "relative.db"); err == nil {
		t.Fatal("backup accepted a relative path")
	}

	restored, err := Open(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err := restored.db.QueryRow("SELECT count(*) FROM traces").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored traces = %d, %v", count, err)
	}
}
