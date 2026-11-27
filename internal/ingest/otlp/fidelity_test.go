package otlp

import (
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestNormalizePreservesOTelContext(t *testing.T) {
	src := fixtureSpan()
	src.Flags = 257
	src.TraceState = "vendor=value"
	src.DroppedAttributesCount, src.DroppedEventsCount, src.DroppedLinksCount = 2, 3, 4
	src.Links = []*trace.Span_Link{{TraceId: src.TraceId, SpanId: []byte{2, 0, 0, 0, 0, 0, 0, 0}, Flags: 1, DroppedAttributesCount: 5}}
	src.Events = []*trace.Span_Event{{Name: "retry", TimeUnixNano: src.StartTimeUnixNano, DroppedAttributesCount: 6,
		Attributes: []*common.KeyValue{{Key: "retry.attempt", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 9007199254740993}}}}}}
	got, err := normalizeSpan(src, "api", nil, telemetry.Scope{}, false, privacy.AttributeSearch{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.OTel.Present || got.OTel.TraceState != "vendor=value" || got.OTel.Flags != 257 || len(got.OTel.Links) != 1 || got.OTel.DroppedLinksCount != 4 {
		t.Fatalf("missing OTLP context: %+v", got.OTel)
	}
	if got.Events[0].DroppedAttributesCount != 6 || got.Events[0].Attributes["retry.attempt"] != int64(9007199254740993) {
		t.Fatalf("event attributes lost: %+v", got.Events)
	}
}

func TestLinkCaptureIsBoundedWithoutRejectingSpan(t *testing.T) {
	src := fixtureSpan()
	for i := 0; i < maxLinks+1; i++ {
		src.Links = append(src.Links, &trace.Span_Link{TraceId: src.TraceId, SpanId: src.SpanId})
	}
	got, err := normalizeSpan(src, "api", nil, telemetry.Scope{}, false, privacy.AttributeSearch{})
	if err != nil || len(got.OTel.Links) != maxLinks || got.OTel.Filtering.Links != 1 || got.OTel.DroppedLinksCount != 0 {
		t.Fatalf("unbounded or misattributed links: %+v, %v", got.OTel, err)
	}
}

func TestAttributeTruncationUsesLocationSpecificLimits(t *testing.T) {
	key := &common.KeyValue{Key: "db.namespace", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: strings.Repeat("x", 400)}}}
	src := fixtureSpan()
	src.Attributes = []*common.KeyValue{key}
	req := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{Resource: &resource.Resource{Attributes: []*common.KeyValue{key}}, ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{src}}}}}}
	spans, rejected, err := normalize(req, false, privacy.AttributeSearch{}, nil, privacy.AttributeRetention{Resource: []string{"db.namespace"}})
	if err != nil || rejected != 0 || len(spans) != 1 {
		t.Fatalf("normalize: %v %d", err, rejected)
	}
	got := spans[0]
	if got.OTel.Filtering.TruncatedValues != 1 || len(got.Attributes["db.namespace"].(string)) != 256 || len(got.ResourceAttributes["db.namespace"].(string)) != 400 {
		t.Fatalf("wrong location limits: %+v", got)
	}
}

func TestSharedEnvelopeFilteringDoesNotAccumulateBetweenSpans(t *testing.T) {
	unknown := &common.KeyValue{Key: "private.unlisted", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "omit"}}}
	req := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{unknown}}, SchemaUrl: "https://opentelemetry.io/schemas/1.30.0",
		ScopeSpans: []*trace.ScopeSpans{{Scope: &common.InstrumentationScope{Attributes: []*common.KeyValue{unknown}}, Spans: []*trace.Span{fixtureSpan(), fixtureSpan()}}},
	}}}
	spans, rejected, err := normalize(req, false, privacy.AttributeSearch{}, nil)
	if err != nil || rejected != 0 || len(spans) != 2 {
		t.Fatalf("normalize: %v %d", err, rejected)
	}
	for _, span := range spans {
		if span.OTel.Filtering.Attributes != 2 || span.OTel.ResourceSchemaURL != "https://opentelemetry.io/schemas/1.30.0" {
			t.Fatalf("shared envelope accounting leaked across spans: %+v", span.OTel)
		}
	}
}

func TestNormalizeInvalidLinksAndStrictContext(t *testing.T) {
	src := fixtureSpan()
	src.TraceState = "vendor=opaque"
	src.Links = []*trace.Span_Link{{TraceId: []byte{1}, SpanId: []byte{2}}}
	got, err := normalizeSpan(src, "api", nil, telemetry.Scope{}, true, privacy.AttributeSearch{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OTel.Links) != 0 || got.OTel.Filtering.Links != 1 || got.OTel.TraceState != "" || got.OTel.Filtering.Metadata != 1 {
		t.Fatalf("invalid link/strict context not accounted: %+v", got.OTel)
	}
	src = fixtureSpan()
	src.Attributes = []*common.KeyValue{{Key: "private.unknown", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "must not persist"}}},
		{Key: "db.namespace", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: strings.Repeat("x", 400)}}}}
	got, err = normalizeSpan(src, "api", nil, telemetry.Scope{}, false, privacy.AttributeSearch{})
	if err != nil || got.OTel.Filtering.Attributes != 1 || got.OTel.Filtering.TruncatedValues != 1 {
		t.Fatalf("filtering = %+v, %v", got.OTel.Filtering, err)
	}
}

func TestCustomRetentionRespectsStrictAndProtectedSearch(t *testing.T) {
	src := fixtureSpan()
	src.Attributes = []*common.KeyValue{
		{Key: "order.id", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "order-42"}}},
		{Key: "custom.enabled", Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: true}}},
	}
	policy := privacy.AttributeRetention{Span: []string{"order.id", "custom.enabled"}}
	search, err := privacy.NewAttributeSearch("order.id", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	got, err := normalizeSpan(src, "api", nil, telemetry.Scope{}, false, search, policy)
	if err != nil || got.Attributes["custom.enabled"] != true {
		t.Fatalf("custom attributes = %+v, %v", got.Attributes, err)
	}
	if _, ok := got.Attributes["order.id"]; ok {
		t.Fatal("custom retention bypassed HMAC")
	}
	if got.Attributes[privacy.AttributeDigestPrefix+"order.id"] == nil {
		t.Fatal("protected search lost")
	}
	got, err = normalizeSpan(src, "api", nil, telemetry.Scope{}, true, search, policy)
	if err != nil || got.Attributes["custom.enabled"] != nil {
		t.Fatalf("strict retained extra attributes: %+v, %v", got.Attributes, err)
	}
}
