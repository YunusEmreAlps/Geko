package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestCompareBaselineScansTracesNewestFirst(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+compareBaselineQuery,
		make([]byte, 16), "api", "GET /", "")
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
	if strings.Contains(steps, "TEMP B-TREE") || strings.Contains(steps, "SCAN r") || !strings.Contains(steps, "SCAN t USING") {
		t.Fatalf("baseline lookup must scan traces in index order:\n%s", steps)
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
	}
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
	if count != 0 || rows != 4 {
		t.Fatalf("trace_services differs from spans: %d mismatches, %d rows", count, rows)
	}
}
