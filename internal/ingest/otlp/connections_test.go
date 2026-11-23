package otlp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestConnectionSamplesOnlyFollowCommittedExports(t *testing.T) {
	var writeErr error
	h := NewHandler(writeFunc(func(context.Context, time.Time, []telemetry.Span) error { return writeErr }))
	attrs := []*common.KeyValue{}
	for key, value := range map[string]string{"service.name": "api", "service.namespace": "store", "deployment.environment.name": "staging", "service.version": "2", "geko.run.id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "private.value": "hidden"} {
		attrs = append(attrs, &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}})
	}
	req := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{Resource: &resource.Resource{Attributes: attrs}, ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{fixtureSpan()}}}}}}
	writeErr = storage.ErrCapacity
	if _, err := h.export(t.Context(), req); err == nil {
		t.Fatal("expected capacity refusal")
	}
	if got := h.Connections(); len(got.Services) != 0 {
		t.Fatalf("refused export recorded: %+v", got)
	}
	writeErr = nil
	if _, err := h.export(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	got := h.Connections()
	if len(got.Services) != 1 {
		t.Fatalf("snapshot: %+v", got)
	}
	sample := got.Services[0]
	if sample.Namespace != "store" || sample.Service != "api" || sample.Environment != "staging" || sample.Version != "2" || sample.RunID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || sample.TraceID == "" || sample.At.IsZero() {
		t.Fatalf("sample: %+v", sample)
	}
	got.Services[0].Service = "modified"
	if h.Connections().Services[0].Service != "api" {
		t.Fatal("snapshot aliases receiver memory")
	}
}

func TestConnectionSamplesAreBoundedAndExpire(t *testing.T) {
	h := NewHandler(nil)
	now := time.Now()
	for i := 0; i < maxConnectionServices+10; i++ {
		h.recordConnections(now.Add(time.Duration(i)*time.Nanosecond), []telemetry.Span{{ServiceName: fmt.Sprintf("svc-%d", i)}})
	}
	got := h.Connections()
	if len(got.Services) != maxConnectionServices || got.Replacements != 10 {
		t.Fatalf("snapshot: %+v", got)
	}
	for _, sample := range got.Services {
		if sample.Service == "svc-0" {
			t.Fatal("oldest identity was not replaced")
		}
	}
	for key, sample := range h.connections {
		sample.At = now.Add(-connectionHistory - time.Second)
		h.connections[key] = sample
	}
	if got := h.Connections(); len(got.Services) != 0 || len(h.connections) != 0 {
		t.Fatalf("expired snapshot: %+v", got)
	}
}

func BenchmarkRecordConnections(b *testing.B) {
	h := NewHandler(nil)
	spans := make([]telemetry.Span, 128)
	for i := range spans {
		spans[i] = telemetry.Span{ServiceName: "api", ResourceAttributes: telemetry.Attributes{"service.namespace": "store"}}
	}
	now := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		now = now.Add(time.Nanosecond)
		h.recordConnections(now, spans)
	}
}
