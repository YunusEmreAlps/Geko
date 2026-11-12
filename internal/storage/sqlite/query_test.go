package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestTraceSummaryUsesIndexedSpanLookups(t *testing.T) {
	store := openTestStore(t)
	traceID := telemetry.TraceID{1}
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+traceSummaryQuery+" WHERE t.trace_id = ?", traceID[:])
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
	if strings.Contains(steps, "MATERIALIZE") || !strings.Contains(steps, "SEARCH r USING PRIMARY KEY") || !strings.Contains(steps, "SEARCH s USING PRIMARY KEY") {
		t.Fatalf("trace summary must look up both spans by primary key:\n%s", steps)
	}
}

func BenchmarkTraceSummary200KSpans(b *testing.B) {
	ctx := b.Context()
	store, err := Open(ctx, filepath.Join(b.TempDir(), "traces.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `WITH RECURSIVE sequence(n) AS (
        VALUES (1) UNION ALL SELECT n+1 FROM sequence WHERE n < 200000
    ) INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns)
      SELECT unhex(printf('%032x', n)), n, n FROM sequence`); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO spans
        (trace_id, span_id, service_name, name, kind, start_ns, end_ns, status_code)
      SELECT trace_id, x'0000000000000001', 'api', 'checkout', 2, 1, 2, 2 FROM traces`); err != nil {
		b.Fatal(err)
	}
	id := telemetry.TraceID{15: 1}
	for b.Loop() {
		summary, _, err := store.Trace(ctx, id, nil, 50)
		if err != nil || summary.EntrySpan == nil || summary.Evidence == nil {
			b.Fatalf("trace summary = %+v, %v", summary, err)
		}
	}
}

func BenchmarkApplicationFailureFilter(b *testing.B) {
	for _, size := range []int{10_000, 100_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			ctx := b.Context()
			store, err := Open(ctx, filepath.Join(b.TempDir(), "applications.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sequence(n) AS (
                VALUES (1) UNION ALL SELECT n+1 FROM sequence WHERE n < ?
            ) INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns)
              SELECT unhex(printf('%032x', n)), n, n FROM sequence`, size); err != nil {
				b.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO spans
                (trace_id, span_id, service_name, name, kind, start_ns, end_ns, status_code, resource_attributes)
              SELECT trace_id, x'0000000000000001', 'api', 'checkout', 2, 1, 2, 2,
                json_object('service.namespace', CASE WHEN last_observed_ns % 100 = 0 THEN 'Reporting' ELSE 'Storefront' END)
              FROM traces`); err != nil {
				b.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			for _, name := range []string{"Storefront", "Reporting", "Missing"} {
				b.Run(name, func(b *testing.B) {
					filter := investigation.FailureFilter{Application: &name, Limit: 50}
					want := 50
					if name == "Missing" {
						want = 0
					}
					for b.Loop() {
						page, err := store.Failures(b.Context(), filter)
						if err != nil || len(page.Items) != want {
							b.Fatalf("application results: %d, %v", len(page.Items), err)
						}
					}
				})
			}
		})
	}
}

func TestFailureEvidenceAndLateRequestOutcome(t *testing.T) {
	store := openTestStore(t)
	observed := time.Unix(100, 0).UTC()
	parent := testSpan(1, 1, "checkout", 200)
	child := testSpan(1, 2, "payments", 503)
	child.ParentID = parent.ID
	child.EndTime = child.StartTime.Add(-time.Nanosecond)
	child.Events = []telemetry.Event{{
		Name: "exception", Time: child.StartTime,
		Attributes: telemetry.Attributes{"exception.message": "payment timed out"},
	}}
	write := func(at time.Time, spans ...telemetry.Span) {
		t.Helper()
		if err := store.WriteSpans(t.Context(), at, spans); err != nil {
			t.Fatal(err)
		}
	}
	write(observed, child)

	filter := investigation.FailureFilter{Limit: 10, Service: "payments"}
	page, err := store.Failures(t.Context(), filter)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("Failures() = %+v, %v", page, err)
	}
	first := page.Items[0]
	if first.HTTPOutcome != "unknown" || first.EntrySpan != nil || first.Evidence == nil ||
		first.Evidence.Classification != "exception" || first.Evidence.ExceptionMessage != "payment timed out" ||
		first.Evidence.DurationNS != nil {
		t.Fatalf("incomplete trace summary = %+v, evidence %+v", first, first.Evidence)
	}

	later := observed.Add(time.Second)
	write(later, parent)
	filter.Service, filter.HTTPStatus, filter.Search = "checkout", 200, "PAYMENT TIMED"
	page, err = store.Failures(t.Context(), filter)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("filtered failures after late parent = %+v, %v", page, err)
	}
	got := page.Items[0]
	if got.HTTPOutcome != "non_5xx" || got.EntrySpan == nil || got.EntrySpan.Service != "checkout" ||
		got.Evidence == nil || got.Evidence.Service != "payments" || !got.FirstObserved.Equal(observed) || !got.LastObserved.Equal(later) {
		t.Fatalf("late parent summary = %+v", got)
	}
	filter.HTTPStatus = 503
	page, err = store.Failures(t.Context(), filter)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("downstream status should not match request status filter: %+v, %v", page, err)
	}

	summary, spans, err := store.Trace(t.Context(), parent.TraceID, nil, 1)
	if err != nil || summary.HTTPOutcome != "non_5xx" || len(spans.Items) != 1 || spans.Next == nil {
		t.Fatalf("first span page = %+v, %+v, %v", summary, spans, err)
	}
	_, next, err := store.Trace(t.Context(), parent.TraceID, spans.Next, 1)
	if err != nil || len(next.Items) != 1 || next.Next != nil || next.Items[0].ParentID != spans.Items[0].SpanID ||
		len(next.Items[0].Events) != 1 || next.Items[0].End.After(next.Items[0].Start) {
		t.Fatalf("second span page = %+v, %v", next, err)
	}

	// Multiple observed server roots make the request outcome ambiguous.
	otherRoot := testSpan(1, 3, "other", 502)
	write(later, otherRoot)
	summary, _, err = store.Trace(t.Context(), parent.TraceID, nil, 1)
	if err != nil || summary.EntrySpan != nil || summary.HTTPOutcome != "unknown" {
		t.Fatalf("ambiguous trace = %+v, %v", summary, err)
	}
}

func TestFailureClassificationAndPagination(t *testing.T) {
	store := openTestStore(t)
	serverError := testSpan(1, 1, "api", 500)
	notFound := testSpan(2, 1, "api", 404)
	explicitError := testSpan(3, 1, "api", 404)
	explicitError.StatusCode = telemetry.StatusError
	exception := testSpan(4, 1, "worker", 0)
	exception.Events = []telemetry.Event{{Name: "exception", Time: exception.StartTime}}
	ordinaryEvent := testSpan(5, 1, "api", 200)
	ordinaryEvent.Events = []telemetry.Event{{Name: "cache miss", Time: ordinaryEvent.StartTime}}
	at := time.Unix(100, 0).UTC()
	if err := store.WriteSpans(t.Context(), at, []telemetry.Span{serverError, notFound, explicitError, exception, ordinaryEvent}); err != nil {
		t.Fatal(err)
	}
	f := investigation.FailureFilter{Limit: 1, From: &at}
	for _, want := range []struct{ classification, outcome string }{
		{"exception", "unknown"},
		{"span_error", "non_5xx"},
		{"http_5xx", "server_error"},
	} {
		page, err := store.Failures(t.Context(), f)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("Failures() = %+v, %v", page, err)
		}
		got := page.Items[0]
		if got.Evidence == nil || got.Evidence.Classification != want.classification || got.HTTPOutcome != want.outcome {
			t.Fatalf("summary = %+v, evidence = %+v, want %+v", got, got.Evidence, want)
		}
		f.After = page.Next
	}
	if f.After != nil {
		t.Fatal("expected the last page")
	}
	before := at.Add(-time.Second)
	page, err := store.Failures(t.Context(), investigation.FailureFilter{Limit: 10, From: &before, To: &at})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("exclusive end bound: %+v, %v", page, err)
	}

	// Evidence membership follows span removal and does not leave stale rows.
	if _, err := store.db.Exec("DELETE FROM spans WHERE trace_id = ?", exception.TraceID[:]); err != nil {
		t.Fatal(err)
	}
	page, err = store.Failures(t.Context(), investigation.FailureFilter{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("failures after span removal = %+v, %v", page, err)
	}
}

func TestMigrationBackfillsExistingSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	_, err = legacy.Exec(initialSchema + fmt.Sprintf("PRAGMA application_id = %d; PRAGMA user_version = 1;", applicationID) + `
INSERT INTO traces VALUES (x'01000000000000000000000000000000', 100, 100);
INSERT INTO spans (trace_id, span_id, service_name, name, kind, start_ns, end_ns, status_code, attributes, events)
VALUES (x'01000000000000000000000000000000', x'0100000000000000', 'legacy', 'GET /test', 2, 100, 120, 0,
    '{"http.response.status_code":500}', '[{"name":"exception","attributes":{"exception.message":"legacy timeout"}}]');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.Failures(t.Context(), investigation.FailureFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("backfilled failures = %+v, %v", page, err)
	}
	got := page.Items[0]
	if got.HTTPOutcome != "server_error" || got.Evidence == nil || got.Evidence.ExceptionMessage != "legacy timeout" {
		t.Fatalf("backfilled summary = %+v, evidence = %+v", got, got.Evidence)
	}
}

func testSpan(traceID, spanID byte, service string, httpStatus int64) telemetry.Span {
	start := time.Unix(0, int64(spanID)*100).UTC()
	span := telemetry.Span{
		TraceID: telemetry.TraceID{traceID}, ID: telemetry.SpanID{spanID},
		ServiceName: service, Name: "GET /test", Kind: telemetry.KindServer,
		StartTime: start, EndTime: start.Add(time.Nanosecond),
	}
	if httpStatus != 0 {
		span.Attributes = telemetry.Attributes{"http.response.status_code": httpStatus}
	}
	return span
}
