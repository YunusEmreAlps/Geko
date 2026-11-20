package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestApplicationNamesIncludesNamesBeyondTwoHundred(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	if _, err := db.db.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (
        VALUES (1) UNION ALL SELECT n + 1 FROM seq WHERE n < 201
    ) INSERT INTO service_applications (service_name, application)
    SELECT printf('service-%03d', n), printf('app-%03d', n) FROM seq`); err != nil {
		t.Fatal(err)
	}
	names, err := db.ApplicationNames(ctx)
	if err != nil || len(names) != 201 || names[200] != "app-201" {
		t.Fatalf("application names count = %d, error = %v", len(names), err)
	}
}

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
	if err := observeSpanTraces(ctx, tx, 100, []telemetry.Span{{TraceID: id}}); err != nil {
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

func TestApplicationNamesDoesNotScanSpans(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+applicationNamesQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(plan, "\n")
	if strings.Contains(steps, "SCAN spans") || !strings.Contains(steps, "spans_namespace") {
		t.Fatalf("application names must walk the namespace index, not scan spans:\n%s", steps)
	}
}

func TestApplicationNamesReportsEveryNamespace(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	now := time.Now()
	for index, namespace := range []string{"Storefront", "Reporting", "Storefront"} {
		var trace telemetry.TraceID
		binary.BigEndian.PutUint64(trace[8:], uint64(index+1))
		span := telemetry.Span{
			TraceID: trace, ID: telemetry.SpanID{byte(index + 1)}, Kind: telemetry.KindServer,
			ServiceName: "gateway", Name: "GET /", StartTime: now, EndTime: now.Add(time.Millisecond),
			ResourceAttributes: telemetry.Attributes{"service.namespace": namespace},
		}
		if err := store.WriteSpans(ctx, now, []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	names, err := store.ApplicationNames(ctx)
	if err != nil || len(names) != 2 || names[0] != "Reporting" || names[1] != "Storefront" {
		t.Fatalf("application names = %v, error = %v", names, err)
	}
}

func BenchmarkApplicationNames(b *testing.B) {
	store := seededStore(b)
	b.ReportAllocs()
	for b.Loop() {
		names, err := store.ApplicationNames(b.Context())
		if err != nil || len(names) == 0 {
			b.Fatalf("application names = %v, error = %v", names, err)
		}
	}
}

func TestUnknownApplicationFilterReturnsNothingWithoutScanning(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	now := time.Now()
	var trace telemetry.TraceID
	binary.BigEndian.PutUint64(trace[8:], 1)
	span := telemetry.Span{
		TraceID: trace, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "gateway", Name: "GET /checkout", StartTime: now, EndTime: now.Add(time.Millisecond),
		StatusCode:         telemetry.StatusError,
		Attributes:         telemetry.Attributes{"http.response.status_code": 503},
		ResourceAttributes: telemetry.Attributes{"service.namespace": "Storefront"},
	}
	if err := store.WriteSpans(ctx, now, []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	stored := "Storefront"
	unknown := "Reporting"
	filter := investigation.FailureFilter{Limit: 25, Application: &stored}
	page, err := store.Failures(ctx, filter)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("stored application page = %+v, error = %v", page, err)
	}
	groups, err := store.FailureGroups(ctx, filter, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("stored application groups = %+v, error = %v", groups, err)
	}
	filter.Application = &unknown
	page, err = store.Failures(ctx, filter)
	if err != nil || len(page.Items) != 0 || page.Next != nil {
		t.Fatalf("unknown application page = %+v, error = %v", page, err)
	}
	groups, err = store.FailureGroups(ctx, filter, nil)
	if err != nil || len(groups.Items) != 0 {
		t.Fatalf("unknown application groups = %+v, error = %v", groups, err)
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO service_applications VALUES ('billing', 'Reporting')"); err != nil {
		t.Fatal(err)
	}
	if known, err := store.knownApplication(ctx, unknown); err != nil || !known {
		t.Fatalf("assigned application known = %v, error = %v", known, err)
	}
}

func TestApplicationExistsUsesTheNamespaceIndex(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+applicationExistsQuery, "Storefront", "Storefront", "Storefront")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(plan, "\n")
	if strings.Contains(steps, "SCAN spans") || !strings.Contains(steps, "spans_namespace") {
		t.Fatalf("application lookup must use the namespace index:\n%s", steps)
	}
}
