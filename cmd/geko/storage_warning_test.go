package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestRejectOvergrownFileWarnsAndCompactRestoresWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overgrown.db")
	db, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	span := telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, Kind: telemetry.KindClient,
		ServiceName: "api", Name: strings.Repeat("x", 2<<20), StartTime: now, EndTime: now.Add(time.Second)}
	if err := db.WriteSpans(t.Context(), now, []telemetry.Span{span}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Prune(t.Context(), now.Add(time.Minute)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.OpenWithPolicy(t.Context(), path, 1<<20, storage.Reject)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	status, err := db.Status(t.Context())
	if err != nil || status.MainBytes <= 1<<20 || status.MainBytes-status.FreeBytes >= 1<<20 {
		t.Fatalf("expected oversized file with reusable pages: %+v: %v", status, err)
	}
	var output bytes.Buffer
	warnStorageLimit(&output, config.Config{MaxDBMiB: 1, OnFull: storage.Reject}, status)
	if !strings.Contains(output.String(), "physical database file") || !strings.Contains(output.String(), "geko compact") {
		t.Fatalf("missing actionable reject warning: %s", output.String())
	}
	span.Name = "small"
	if err := db.WriteSpans(t.Context(), now, []telemetry.Span{span}); !errors.Is(err, storage.ErrCapacity) {
		t.Fatalf("reject above physical limit: %v", err)
	}
	if err := db.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteSpans(t.Context(), now, []telemetry.Span{span}); err != nil {
		t.Fatalf("write after compact: %v", err)
	}
}
