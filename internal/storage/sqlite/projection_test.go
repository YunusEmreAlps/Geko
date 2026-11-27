package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func queryPlan(t *testing.T, store *Store, query string, args ...any) string {
	t.Helper()
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
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
	return strings.Join(plan, "\n")
}

func TestCompareBaselineScansRecentTracesNewestFirst(t *testing.T) {
	steps := queryPlan(t, openTestStore(t), recentBaselineQuery, recentBaselineTraces, make([]byte, 16), "api", "GET /", "")
	if strings.Contains(steps, "TEMP B-TREE") || strings.Contains(steps, "SCAN r") || !strings.Contains(steps, "SCAN traces USING") {
		t.Fatalf("recent baseline lookup must scan traces in index order:\n%s", steps)
	}
}

func TestCompareBaselineLooksTheOperationUp(t *testing.T) {
	steps := queryPlan(t, openTestStore(t), operationBaselineQuery, make([]byte, 16), "api", "GET /", "")
	if !strings.Contains(steps, "SEARCH r USING INDEX spans_entry_operations (service_name=? AND name=?)") || strings.Contains(steps, "SCAN") {
		t.Fatalf("operation baseline lookup must search the entry index:\n%s", steps)
	}
}

func TestTraceServicesMatchStoredSpans(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	now := time.Now()
	span := func(trace, id byte, service, namespace string) telemetry.Span {
		resource := telemetry.Attributes{"service.name": service}
		if namespace != "" {
			resource["service.namespace"] = namespace
		}
		return telemetry.Span{
			TraceID: telemetry.TraceID{trace}, ID: telemetry.SpanID{id}, ServiceName: service, Name: "work",
			StartTime: now, EndTime: now.Add(time.Millisecond), ResourceAttributes: resource,
		}
	}
	exports := [][]telemetry.Span{
		{span(1, 1, "api", "Shop"), span(1, 2, "api", "Shop"), span(1, 3, "db", ""), span(2, 1, "api", "Shop")},
		// Retransmitted spans are ignored; whitespace-only namespaces resolve to none.
		{span(1, 1, "api", "Shop"), span(1, 4, "db", " "), span(1, 5, "api", "Shop"), span(2, 2, "api", "Reports")},
		// Ignore conflicting resource metadata both within a chunk and against
		// previously stored spans. Only the inserted row owns membership.
		{span(250, 1, "worker", " Original "), span(250, 1, "different", "Other"), span(1, 1, "different", "Other")},
	}
	// Exercise both the full and trailing chunks of trace and service writes.
	multipleChunks := make([]telemetry.Span, 0, writeChunkSize+1)
	for i := range writeChunkSize + 1 {
		multipleChunks = append(multipleChunks, span(byte(i+3), 1, "api", "Shop"))
	}
	exports = append(exports, multipleChunks, multipleChunks)
	for _, export := range exports {
		if err := store.WriteSpans(ctx, now, export); err != nil {
			t.Fatal(err)
		}
	}
	const mismatches = `
WITH projected AS (SELECT trace_id, namespace, service_name, span_count FROM trace_services),
     recomputed AS (SELECT trace_id, service_namespace, service_name, count(*) FROM spans GROUP BY 1, 2, 3)
SELECT (SELECT count(*) FROM (SELECT * FROM projected EXCEPT SELECT * FROM recomputed))
     + (SELECT count(*) FROM (SELECT * FROM recomputed EXCEPT SELECT * FROM projected))`
	var count, rows int
	if err := store.read.QueryRowContext(ctx, mismatches).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := store.read.QueryRowContext(ctx, "SELECT count(*) FROM trace_services").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if count != 0 || rows != 5+len(multipleChunks) {
		t.Fatalf("trace_services differs from spans: %d mismatches, %d rows", count, rows)
	}
}

func TestBulkSpanWriteRollsBackEarlierChunks(t *testing.T) {
	store := openTestStore(t)
	now := time.Now()
	spans := make([]telemetry.Span, writeChunkSize+1)
	for i := range spans {
		spans[i] = testSpan(byte(i+1), 1, "api", 503)
	}
	spans[len(spans)-1].Attributes["invalid"] = make(chan int)
	if err := store.WriteSpans(t.Context(), now, spans); err == nil {
		t.Fatal("expected metadata encoding failure")
	}
	for _, table := range []string{"traces", "spans", "span_evidence", "trace_services", "failure_group_members"} {
		var count int
		if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after rollback = %d, %v", table, count, err)
		}
	}
	delete(spans[len(spans)-1].Attributes, "invalid")
	if err := store.WriteSpans(t.Context(), now, spans); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	var count int
	if err := store.read.QueryRowContext(t.Context(), "SELECT sum(span_count) FROM trace_services").Scan(&count); err != nil ||
		count != len(spans) {
		t.Fatalf("retry membership = %d, %v", count, err)
	}
}

func TestExplicitBaselineUsesPointLookups(t *testing.T) {
	steps := queryPlan(t, openTestStore(t), selectedBaselineQuery, make([]byte, 16), "api", "GET /", "", make([]byte, 16))
	if strings.Contains(steps, "SCAN ") || strings.Contains(steps, "TEMP B-TREE") || !strings.Contains(steps, "SEARCH t ") {
		t.Fatalf("manual reference must use point lookups:\n%s", steps)
	}
}
