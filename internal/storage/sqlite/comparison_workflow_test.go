package sqlite

import (
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestComparisonChoicesBoundedWithIdenticalObservedTimes(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "choices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	planRows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+comparisonChoicesQuery, int64(0), investigation.ChoiceTraceLimit+1, investigation.ChoiceSpanLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for planRows.Next() {
		var a, b, c int
		var detail string
		if err := planRows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	planRows.Close()
	if !strings.Contains(plan.String(), "traces_last_observed") || strings.Contains(plan.String(), "SCAN s ") || strings.Contains(plan.String(), "SCAN spans") || strings.Contains(plan.String(), "TEMP B-TREE") {
		t.Fatalf("unbounded suggestion plan: %s", plan.String())
	}
	now := time.Now()
	spans := make([]telemetry.Span, investigation.ChoiceTraceLimit+2)
	for i := range spans {
		var id telemetry.TraceID
		binary.BigEndian.PutUint64(id[8:], uint64(i+1))
		spans[i] = telemetry.Span{TraceID: id, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /", Kind: telemetry.KindServer, StartTime: now, EndTime: now.Add(time.Millisecond), ResourceAttributes: telemetry.Attributes{"service.namespace": "shop", "service.version": "v1", "geko.run.id": hex.EncodeToString(id[:])}}
	}
	if err := store.WriteSpans(t.Context(), now, spans); err != nil {
		t.Fatal(err)
	}
	got, err := store.ComparisonChoices(t.Context(), time.Time{}, "api")
	if err != nil || !got.Truncated || len(got.Runs) != investigation.ChoiceLimit || len(got.Versions) != 1 || got.Versions[0].Value != "v1" {
		t.Fatalf("bounded choices: %+v %v", got, err)
	}
	got, err = store.ComparisonChoices(t.Context(), now.Add(time.Hour), "")
	if err != nil || len(got.Runs) != 0 || len(got.Versions) != 0 {
		t.Fatalf("time filter ignored: %+v %v", got, err)
	}
	got, err = store.ComparisonChoices(t.Context(), time.Time{}, "missing")
	if err != nil || len(got.Runs) != 0 || len(got.Versions) != 0 {
		t.Fatalf("service filter ignored: %+v %v", got, err)
	}
}

func TestTraceComparisonBoundsSpanFacts(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "comparison.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	root := telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /", Kind: telemetry.KindServer, StartTime: now, EndTime: now.Add(time.Second), Attributes: telemetry.Attributes{"http.response.status_code": 200}}
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{root}); err != nil {
		t.Fatal(err)
	}
	root.TraceID = telemetry.TraceID{2}
	root.StatusCode = telemetry.StatusError
	spans := []telemetry.Span{root}
	for i := 1; i <= investigation.MapSpanLimit; i++ {
		span := root
		binary.BigEndian.PutUint64(span.ID[:], uint64(i+2))
		span.ParentID = root.ID
		span.Kind = telemetry.KindClient
		span.Name = "query"
		span.ResourceAttributes = telemetry.Attributes{"service.namespace": "", "deployment.environment.name": "prod"}
		spans = append(spans, span)
	}
	if err := store.WriteSpans(t.Context(), now, spans); err != nil {
		t.Fatal(err)
	}
	got, err := store.TraceComparison(t.Context(), root.TraceID)
	if err != nil || got.Baseline == nil || !got.Truncated || len(got.SourceSpans) != investigation.MapSpanLimit || len(got.BaselineSpans) != 1 {
		t.Fatalf("comparison counts: source=%d baseline=%d truncated=%v err=%v", len(got.SourceSpans), len(got.BaselineSpans), got.Truncated, err)
	}
	manual, err := store.TraceComparisonWithBaseline(t.Context(), root.TraceID, telemetry.TraceID{1})
	if err != nil || !manual.Truncated || len(manual.SourceSpans) != investigation.MapSpanLimit || len(manual.BaselineSpans) != 1 {
		t.Fatalf("manual bounds: %+v %v", manual, err)
	}
	reader, err := OpenReadOnly(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if comparison, err := reader.TraceComparisonWithBaseline(t.Context(), root.TraceID, telemetry.TraceID{1}); err != nil || comparison.Baseline == nil {
		t.Fatalf("read-only selection: %+v %v", comparison, err)
	}
	if !got.SourceSpans[0].HasEvidence {
		t.Error("evidence flag missing")
	}
}
