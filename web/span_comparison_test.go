package web

import (
	"math"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

func TestOperationComparisonKeepsIdentitiesAndRepeatedCalls(t *testing.T) {
	duration := int64(100)
	a := investigation.ComparisonSpan{SpanSummary: investigation.SpanSummary{Operation: "query", Service: "api", Application: "shop", SpanID: "a", DurationNS: &duration}, Namespace: "shop", Environment: "prod"}
	b := a
	b.HasEvidence = true
	b.SpanID = "b"
	other := a
	other.Environment = "staging"
	rows := comparisonRows([]investigation.ComparisonSpan{a, b, other}, []investigation.ComparisonSpan{a})
	if len(rows) != 2 {
		t.Fatalf("merged separate environments: %+v", rows)
	}
	var found bool
	for _, row := range rows {
		if row.Environment == "prod" {
			found = true
			if row.Source.Calls != 2 || row.Source.Errors != 1 || row.Source.SpanID != "b" || row.Baseline.Calls != 1 {
				t.Fatalf("lost repeats/evidence: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("missing prod row")
	}
}

func TestOperationComparisonDoesNotInventDurationsOrMergeKinds(t *testing.T) {
	value := int64(math.MaxInt64)
	span := investigation.ComparisonSpan{SpanSummary: investigation.SpanSummary{Service: "api", Operation: "work", DurationNS: &value}}
	rows := comparisonRows([]investigation.ComparisonSpan{span, span}, nil)
	if rows[0].Source.DurationNS != nil {
		t.Error("overflowed duration must be unavailable")
	}
	missing := span
	missing.DurationNS = nil
	rows = comparisonRows([]investigation.ComparisonSpan{span, missing}, nil)
	if rows[0].Source.DurationNS != nil {
		t.Error("partial timing must not appear complete")
	}
	other := span
	other.Kind = 2
	if rows = comparisonRows([]investigation.ComparisonSpan{span, other}, nil); len(rows) != 2 {
		t.Error("different span kinds were merged")
	}
}
