package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestExplicitBaselineSelectionAndEligibility(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	root := telemetry.Span{ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /checkout", Kind: telemetry.KindServer, StartTime: now, EndTime: now.Add(time.Millisecond), Attributes: telemetry.Attributes{"http.response.status_code": 200}, ResourceAttributes: telemetry.Attributes{"service.namespace": "shop", "deployment.environment.name": "prod"}}
	for i := byte(1); i <= 9; i++ {
		span := root
		span.TraceID = telemetry.TraceID{i}
		switch i {
		case 3:
			span.StatusCode = telemetry.StatusError
		case 4:
			span.Name = "other"
		case 5:
			span.ResourceAttributes = telemetry.Attributes{"service.namespace": "other", "deployment.environment.name": "prod"}
		case 6:
			span.ResourceAttributes = telemetry.Attributes{"service.namespace": "shop", "deployment.environment.name": "staging"}
		case 7:
			span.ServiceName = "other-api"
		case 8:
			span.Attributes = telemetry.Attributes{"http.response.status_code": 500}
		case 9:
			span.Kind = telemetry.KindClient
		}
		if err := store.WriteSpans(t.Context(), now.Add(time.Duration(i)*time.Second), []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	source := telemetry.TraceID{3}
	automatic, err := store.TraceComparison(t.Context(), source)
	if err != nil || automatic.Baseline == nil || automatic.Baseline.TraceID != "02000000000000000000000000000000" {
		t.Fatalf("auto: %+v %v", automatic, err)
	}
	selected, err := store.TraceComparisonWithBaseline(t.Context(), source, telemetry.TraceID{1})
	if err != nil || selected.Baseline == nil || selected.Baseline.TraceID != "01000000000000000000000000000000" || len(selected.SourceSpans) != 1 || len(selected.BaselineSpans) != 1 {
		t.Fatalf("manual: %+v %v", selected, err)
	}
	for _, i := range []byte{3, 4, 5, 6, 7, 8, 9} {
		result, err := store.TraceComparisonWithBaseline(t.Context(), source, telemetry.TraceID{i})
		if !errors.Is(err, investigation.ErrBaselineIneligible) || result.Baseline != nil || len(result.BaselineSpans) != 0 {
			t.Fatalf("ineligible %d: %+v %v", i, result, err)
		}
	}
	if result, err := store.TraceComparisonWithBaseline(t.Context(), source, telemetry.TraceID{99}); !errors.Is(err, investigation.ErrBaselineNotFound) || result.Source.TraceID == "" {
		t.Fatalf("missing: %+v %v", result, err)
	}
	if _, err := store.TraceComparisonWithBaseline(t.Context(), telemetry.TraceID{99}, telemetry.TraceID{1}); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatal(err)
	}
	if _, err := store.TraceComparisonWithBaseline(t.Context(), telemetry.TraceID{2}, telemetry.TraceID{1}); !errors.Is(err, investigation.ErrComparisonSource) {
		t.Fatal(err)
	}
}
