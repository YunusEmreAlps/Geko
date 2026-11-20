package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestOverviewRootAndEvidenceFallback(t *testing.T) {
	store := openTestStore(t)
	root := testSpan(1, 1, "gateway", 200)
	root.ResourceAttributes = telemetry.Attributes{"service.namespace": "shop"}
	child := testSpan(1, 2, "payments", 500)
	child.ParentID = root.ID
	fallback := testSpan(2, 1, "worker", 500)
	fallback.Kind = telemetry.KindInternal
	observed := time.Now()
	if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{root, child, fallback}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Overview(t.Context(), observed.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceCount != 2 || got.ServiceCount != 2 || len(got.Operations) != 2 {
		t.Fatalf("overview = %+v", got)
	}
	services := map[string]string{}
	for _, op := range got.Operations {
		services[op.Service] = op.Application
	}
	if services["gateway"] != "shop" {
		t.Fatalf("root application: %+v", services)
	}
	if _, ok := services["worker"]; !ok {
		t.Fatalf("missing evidence fallback: %+v", services)
	}
	empty, err := store.Overview(t.Context(), observed.Add(time.Hour))
	if err != nil || empty.TraceCount != 0 {
		t.Fatalf("time filter = %+v, %v", empty, err)
	}
}

func TestOverviewCountsEveryTraceAndSamplesTheBreakdown(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "overview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	// The oldest hundred traces record their own operation, so a breakdown of
	// the most recent OverviewSampleLimit traces leaves it out while the count
	// still covers them.
	traces := OverviewSampleLimit + 100
	if _, err := store.db.ExecContext(ctx, `WITH RECURSIVE sequence(n) AS (
        VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n < ?
    ) INSERT INTO traces(trace_id, first_observed_ns, last_observed_ns)
      SELECT unhex(printf('%032x', n)), n, n FROM sequence`, traces); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO spans
        (trace_id, span_id, service_name, name, kind, start_ns, end_ns, status_code)
      SELECT trace_id, x'0000000000000001', 'api',
        CASE WHEN last_observed_ns > ? THEN 'POST /checkout' ELSE 'GET /oldest' END, 2, 1, 2, 2
      FROM traces`, traces-OverviewSampleLimit); err != nil {
		t.Fatal(err)
	}
	got, err := store.Overview(ctx, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceCount != traces {
		t.Fatalf("trace count = %d, want every trace (%d)", got.TraceCount, traces)
	}
	if got.SampledTraces != OverviewSampleLimit {
		t.Fatalf("sampled traces = %d, want %d", got.SampledTraces, OverviewSampleLimit)
	}
	if len(got.Operations) != 1 || got.Operations[0].Operation != "POST /checkout" {
		t.Fatalf("breakdown = %+v, want the sampled operation only", got.Operations)
	}
	if got.Operations[0].Count != OverviewSampleLimit {
		t.Fatalf("breakdown count = %d, want the sample size", got.Operations[0].Count)
	}
}

func TestOverviewReportsNoSampleWithinTheLimit(t *testing.T) {
	store := openTestStore(t)
	span := testSpan(1, 1, "gateway", 500)
	observed := time.Now()
	if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Overview(t.Context(), observed.Add(-time.Hour))
	if err != nil || got.TraceCount != 1 || got.SampledTraces != 0 {
		t.Fatalf("overview = %+v, %v", got, err)
	}
}

func BenchmarkOverview200KFailures(b *testing.B) {
	store, err := Open(b.Context(), filepath.Join(b.TempDir(), "overview.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(b.Context(), `WITH RECURSIVE sequence(n) AS (
 VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n < 200000
 ) INSERT INTO traces(trace_id,first_observed_ns,last_observed_ns)
 SELECT unhex(printf('%032x',n)),n,n FROM sequence;
 INSERT INTO spans(trace_id,span_id,service_name,name,kind,start_ns,end_ns,status_code)
 SELECT trace_id,x'0000000000000001','api','checkout',2,1,2,2 FROM traces;`); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		got, err := store.Overview(b.Context(), time.Unix(0, 0))
		if err != nil || got.TraceCount != 200000 {
			b.Fatalf("overview=%+v, %v", got, err)
		}
	}
}
