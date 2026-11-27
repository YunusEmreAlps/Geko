package otlp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestOTelExportRestoreFidelityAndStrictBoundary(t *testing.T) {
	id := telemetry.TraceID{1}
	scope := telemetry.Scope{Name: "fixture", Version: "1", Attributes: telemetry.Attributes{"scope.count": json.Number("9007199254740993")}}
	span := investigation.Span{SpanSummary: investigation.SpanSummary{SpanID: "0200000000000000", Service: "api", Operation: "test"},
		Start: time.Unix(1, 0), End: time.Unix(1, 100), Scope: scope,
		ResourceAttributes: telemetry.Attributes{"service.name": "api", "project.revision": "v1"},
		Attributes:         telemetry.Attributes{"custom.ratio": json.Number("1.0")},
		Events:             []telemetry.Event{{Name: "retry", Time: time.Unix(1, 0), DroppedAttributesCount: 9, Attributes: telemetry.Attributes{"retry.attempt": json.Number("9007199254740993"), "retry.reason": "capacity"}}},
		OTel: telemetry.Metadata{Present: true, Filtering: telemetry.Filtering{Attributes: 12, TruncatedValues: 3}, TraceState: "vendor=value", Flags: 257, ResourceSchemaURL: "https://opentelemetry.io/schemas/1.30.0", ScopeSchemaURL: "https://opentelemetry.io/schemas/1.29.0", DroppedAttributesCount: 2, DroppedEventsCount: 3, DroppedLinksCount: 4, ResourceDroppedAttributesCount: 5, ScopeDroppedAttributesCount: 6,
			Links: []telemetry.Link{{TraceID: hex.EncodeToString(id[:]), SpanID: "0300000000000000", TraceState: "vendor=linked", Flags: 1, DroppedAttributesCount: 7, Attributes: telemetry.Attributes{"link.kind": "batch"}}}}}
	policy := privacy.AttributeRetention{Resource: []string{"project.revision"}, Scope: []string{"scope.count"}, Span: []string{"custom.ratio"}, Event: []string{"retry.reason"}, Link: []string{"link.kind"}}
	for _, strict := range []bool{false, true} {
		body, err := EncodeTraceJSON(id, strict, func(visit func(investigation.Span) error) error { return visit(span) }, policy)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "AQAAAAAAAAAAAAAAAAAAAA==") {
			t.Fatal("link trace ID exported as base64")
		}
		var request collector.ExportTraceServiceRequest
		if err := decodeJSON(body, &request); err != nil {
			t.Fatal(err)
		}
		spans, rejected, err := normalize(&request, strict, privacy.AttributeSearch{}, nil, policy)
		if err != nil || rejected != 0 || len(spans) != 1 {
			t.Fatalf("roundtrip: %d %v", rejected, err)
		}
		got := spans[0]
		if len(got.OTel.Links) != 1 || got.OTel.Flags != 257 || got.OTel.DroppedLinksCount != 4 || got.OTel.ResourceDroppedAttributesCount != 5 || got.OTel.ScopeDroppedAttributesCount != 6 || got.OTel.ResourceSchemaURL != span.OTel.ResourceSchemaURL || got.Events[0].DroppedAttributesCount != 9 {
			t.Fatalf("OTLP fields lost: %+v", got)
		}
		if got.OTel.Filtering.Attributes < 12 || got.OTel.Filtering.TruncatedValues < 3 {
			t.Fatalf("lost source filtering: %+v", got.OTel.Filtering)
		}
		if strict {
			if got.OTel.TraceState != "" || got.OTel.Links[0].TraceState != "" || got.OTel.Links[0].Attributes["link.kind"] != nil || got.Events[0].Attributes["retry.reason"] != nil || got.Scope.Attributes["scope.count"] != nil {
				t.Fatalf("strict leaked extra values: %+v", got)
			}
		} else if got.OTel.Links[0].TraceState != "vendor=linked" || got.OTel.Links[0].Attributes["link.kind"] != "batch" || got.Scope.Attributes["scope.count"] != int64(9007199254740993) || got.Attributes["custom.ratio"] != json.Number("1.0") || got.Events[0].Attributes["retry.attempt"] != int64(9007199254740993) {
			t.Fatalf("attribute precision/context lost: %+v", got)
		}
	}
}

func TestLegacyTransferDoesNotInventCaptureCoverage(t *testing.T) {
	id := telemetry.TraceID{1}
	span := investigation.Span{SpanSummary: investigation.SpanSummary{SpanID: "0200000000000000", Service: "api", Operation: "legacy"}, Start: time.Unix(1, 0), End: time.Unix(1, 100)}
	body, err := EncodeTraceJSON(id, false, func(visit func(investigation.Span) error) error { return visit(span) })
	if err != nil {
		t.Fatal(err)
	}
	var req collector.ExportTraceServiceRequest
	if err := decodeJSON(body, &req); err != nil {
		t.Fatal(err)
	}
	spans, _, err := normalize(&req, false, privacy.AttributeSearch{}, nil)
	if err != nil || len(spans) != 1 || spans[0].OTel.Present {
		t.Fatalf("invented legacy coverage: %+v %v", spans, err)
	}
}

func TestWholeAndNegativeZeroDoublesSurviveStoredJSON(t *testing.T) {
	for _, input := range []float64{1, math.Copysign(0, -1), 1.25} {
		src := fixtureSpan()
		src.Attributes = []*common.KeyValue{{Key: "custom.double", Value: &common.AnyValue{Value: &common.AnyValue_DoubleValue{DoubleValue: input}}}}
		span, err := normalizeSpan(src, "api", nil, telemetry.Scope{}, false, privacy.AttributeSearch{}, privacy.AttributeRetention{Span: []string{"custom.double"}})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := json.Marshal(span.Attributes)
		if err != nil {
			t.Fatal(err)
		}
		var attrs telemetry.Attributes
		decoder := json.NewDecoder(bytes.NewReader(stored))
		decoder.UseNumber()
		if err := decoder.Decode(&attrs); err != nil {
			t.Fatal(err)
		}
		wire := transferAttributes(attrs)
		if len(wire) != 1 {
			t.Fatalf("missing value: %s", stored)
		}
		value, ok := wire[0].Value.Value.(*common.AnyValue_DoubleValue)
		if !ok || value.DoubleValue != input || math.Signbit(value.DoubleValue) != math.Signbit(input) {
			t.Fatalf("double changed type/value: %s %+v", stored, wire)
		}
	}
}
