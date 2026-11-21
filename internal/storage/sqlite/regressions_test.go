package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestVersionComparisonReadsTheVersionIndex(t *testing.T) {
	store := openTestStore(t)
	steps := queryPlan(t, store, versionComparisonQuery,
		"1.0.0", 0, "", "", 10, "1.1.0", 0, "", "", 10, 10, 10, 10, 10, 10, "1.0.0", "1.1.0")
	if strings.Count(steps, "SEARCH t USING INDEX traces_entry_versions (entry_version=? AND last_observed_ns>?)") != 2 ||
		strings.Contains(steps, "SCAN t") {
		t.Fatalf("each version must be read newest first from its index range:\n%s", steps)
	}
}

func versionedRoot(trace uint32, version string, failing bool) []telemetry.Span {
	var id telemetry.TraceID
	id[12], id[13], id[14], id[15] = byte(trace>>24), byte(trace>>16), byte(trace>>8), byte(trace)
	now := time.Now()
	root := telemetry.Span{
		TraceID: id, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer, ServiceName: "orders", Name: "GET /orders",
		StartTime: now, EndTime: now.Add(time.Millisecond),
		Attributes:         telemetry.Attributes{"http.response.status_code": int64(200)},
		ResourceAttributes: telemetry.Attributes{"service.version": version},
	}
	if !failing {
		return []telemetry.Span{root}
	}
	child := root
	child.ID, child.ParentID, child.StatusCode, child.Kind = telemetry.SpanID{2}, root.ID, telemetry.StatusError, telemetry.KindInternal
	return []telemetry.Span{root, child}
}

func TestEntryVersionFollowsTheUniqueEntrySpan(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	spans := versionedRoot(1, "2.0.0", false)
	if err := store.WriteSpans(ctx, time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	version := func() any {
		var value any
		if err := store.read.QueryRowContext(ctx, "SELECT entry_version FROM traces WHERE trace_id = ?", spans[0].TraceID[:]).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := version(); got != "2.0.0" {
		t.Fatalf("entry version = %v", got)
	}
	second := spans[0]
	second.ID = telemetry.SpanID{9}
	if err := store.WriteSpans(ctx, time.Now(), []telemetry.Span{second}); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != nil {
		t.Fatalf("a trace with two entry spans has no entry version, got %v", got)
	}
}

func TestVersionComparisonSamplesTheNewestTraces(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	var batch []telemetry.Span
	flush := func() {
		if err := store.WriteSpans(ctx, time.Now(), batch); err != nil {
			t.Fatal(err)
		}
		batch = batch[:0]
	}
	// The oldest 100 baseline traces fail and fall outside the sample.
	for i := range investigation.RegressionSampleLimit + 100 {
		batch = append(batch, versionedRoot(uint32(i+1), "1.0.0", i < 100)...)
		if len(batch) >= 500 || i < 100 && len(batch) >= 100*2 {
			flush()
		}
	}
	flush()
	for i := range 20 {
		batch = append(batch, versionedRoot(uint32(1_000_000+i), "1.1.0", i%2 == 0)...)
	}
	flush()
	rows, err := store.VersionComparisons(ctx, investigation.RegressionFilter{
		Baseline: "1.0.0", Candidate: "1.1.0", Since: time.Now().Add(-time.Hour)})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	row := rows[0]
	if row.Baseline.Traces != investigation.RegressionSampleLimit || row.Baseline.Failures != 0 || !row.Baseline.Sampled {
		t.Fatalf("baseline = %+v", row.Baseline)
	}
	if row.Candidate.Traces != 20 || row.Candidate.Failures != 10 || row.Candidate.Sampled || !row.FailureRegression {
		t.Fatalf("candidate = %+v, regression %v", row.Candidate, row.FailureRegression)
	}
}
