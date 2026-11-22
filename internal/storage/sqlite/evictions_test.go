package sqlite

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestApplicationEvictionsAreAtomicAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "losses.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	base := time.Now().Add(-time.Hour)
	span := func(trace, id byte, service, namespace string) telemetry.Span {
		s := testSpan(trace, id, service, 503)
		s.ResourceAttributes = telemetry.Attributes{"service.namespace": namespace}
		return s
	}
	if err := store.SetServiceApplication(t.Context(), "mapped-api", "mapped"); err != nil {
		t.Fatal(err)
	}
	for i, batch := range [][]telemetry.Span{
		{span(1, 1, "api", "alpha"), span(1, 2, "worker", "beta"), span(1, 3, "db", "alpha")},
		{span(2, 1, "mapped-api", "")}, {span(3, 1, "legacy", "")},
		{span(4, 1, "api", "pinned")}, {span(5, 1, "unknown", "")},
	} {
		if err := store.WriteSpans(t.Context(), base.Add(time.Duration(i)*time.Minute), batch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AssignTraceApplication(t.Context(), base, base.Add(time.Hour), "legacy", "assigned"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTracePin(t.Context(), telemetry.TraceID{4}, true, "tester", base); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	bad := testSpan(99, 1, "incoming", 200)
	bad.Attributes["bad"] = make(chan int)
	if _, err := store.writeSpanBatch(t.Context(), now.UnixNano(), []telemetry.Span{bad}, "[]", 4); err == nil {
		t.Fatal("expected the incoming export to fail after eviction")
	}
	usage, err := store.StorageByApplication(t.Context())
	if err != nil || usage.EvictedTraces != 0 {
		t.Fatalf("failed export retained losses: %+v: %v", usage, err)
	}
	for _, app := range usage.Applications {
		if app.EvictedTraces != 0 {
			t.Fatalf("rolled-back application counter: %+v", app)
		}
	}
	assertGroupTotals(t, store)
	good := testSpan(99, 1, "incoming", 200)
	deleted, err := store.writeSpanBatch(t.Context(), now.UnixNano(), []telemetry.Span{good}, "[]", 4)
	if err != nil || deleted != 4 {
		t.Fatalf("eviction = %d: %v", deleted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = store.StorageByApplication(t.Context())
	if err != nil || usage.EvictedTraces != 4 || usage.EarlierEvictions != 0 {
		t.Fatalf("persisted usage: %+v: %v", usage, err)
	}
	losses := map[string]int64{}
	for _, app := range usage.Applications {
		losses[app.Application] = app.EvictedTraces
	}
	for _, application := range []string{"alpha", "beta", "mapped", "assigned", ""} {
		if losses[application] != 1 {
			t.Fatalf("application %q lost %d traces, want 1", application, losses[application])
		}
	}
	if losses["pinned"] != 0 {
		t.Fatal("pinned trace was attributed as evicted")
	}
	if _, _, err := store.Trace(t.Context(), telemetry.TraceID{1}, nil, 10); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("evicted trace remains: %v", err)
	}
	if err := store.SetServiceApplication(t.Context(), "mapped-api", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTracePin(t.Context(), telemetry.TraceID{4}, false, "tester", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	usage, err = store.StorageByApplication(t.Context())
	if err != nil || usage.Spans != 0 || usage.EvictedTraces != 4 || len(usage.Applications) != 5 {
		t.Fatalf("retention changed losses or hid empty applications: %+v: %v", usage, err)
	}
	for _, app := range usage.Applications {
		if app.Share != 0 || app.Application == "renamed" {
			t.Fatalf("historical loss relabelled or invalid empty share: %+v", app)
		}
	}
	assertGroupTotals(t, store)
}

func TestApplicationEvictionBudgetKeepsOverflowVisible(t *testing.T) {
	store := openTestStore(t)
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range maxEvictionApplications {
		name := ""
		if i > 0 {
			name = fmt.Sprintf("app-%03d", i)
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO application_evictions VALUES(?,1)", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), "UPDATE storage_counters SET evicted_traces=? WHERE id=1", maxEvictionApplications); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	var spans []telemetry.Span
	for i, label := range []string{"", "new-untracked", strings.Repeat("界", 200)} {
		span := testSpan(1, byte(i+1), "api", 503)
		span.ResourceAttributes = telemetry.Attributes{"service.namespace": label}
		spans = append(spans, span)
	}
	if err := store.WriteSpans(t.Context(), old, spans); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	bad := testSpan(2, 1, "incoming", 200)
	bad.Attributes["bad"] = make(chan int)
	if _, err := store.writeSpanBatch(t.Context(), now.UnixNano(), []telemetry.Span{bad}, "[]", 1); err == nil {
		t.Fatal("expected rollback")
	}
	usage, err := store.StorageByApplication(t.Context())
	if err != nil || usage.EvictionOverflow != 0 || usage.EvictedTraces != maxEvictionApplications {
		t.Fatalf("overflow survived rollback: %+v: %v", usage, err)
	}
	if _, err := store.writeSpanBatch(t.Context(), now.UnixNano(), []telemetry.Span{testSpan(2, 1, "incoming", 200)}, "[]", 1); err != nil {
		t.Fatal(err)
	}
	usage, err = store.StorageByApplication(t.Context())
	if err != nil || usage.EvictionOverflow != 2 || usage.EvictedTraces != maxEvictionApplications+1 {
		t.Fatalf("overflow/global counts: %+v: %v", usage, err)
	}
	var labels int
	if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM application_evictions").Scan(&labels); err != nil || labels != maxEvictionApplications {
		t.Fatalf("unbounded labels: %d: %v", labels, err)
	}
	var unassigned int
	if err := store.read.QueryRowContext(t.Context(), "SELECT evicted_traces FROM application_evictions WHERE application=''").Scan(&unassigned); err != nil || unassigned != 2 {
		t.Fatalf("No application was merged into overflow: %d: %v", unassigned, err)
	}
}
