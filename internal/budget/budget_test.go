package budget

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestBudgetWindowsLimitsAndPrivateCardinality(t *testing.T) {
	now := time.Date(2026, 11, 17, 11, 10, 0, 0, time.UTC)
	tracker := New(Config{Enabled: true, Defaults: Limits{Spans: 1, Bytes: 1},
		Services:   []ServiceLimit{{Namespace: "billing", Service: "api", Limits: Limits{Spans: 4}}},
		Attributes: []string{"customer.id"}, CardinalityLimit: 1,
	}, now)
	var expectedBytes int64
	for i, namespace := range []string{"billing", "store", "store", "store"} {
		raw := &trace.Span{Attributes: []*common.KeyValue{{
			Key: "customer.id", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: fmt.Sprintf("private-value-%d", i%2)}},
		}}}
		if namespace == "store" {
			expectedBytes += int64(proto.Size(raw))
		}
		tracker.Record(now, []telemetry.Span{{ServiceName: "api",
			ResourceAttributes: telemetry.Attributes{"service.namespace": namespace}}},
			[]Observation{{Span: raw}})
	}
	report := tracker.Snapshot(now, false)
	if report.Total.Spans != 4 || len(report.Services) != 2 || !report.ObservedSince.Equal(now) {
		t.Fatalf("report = %+v", report)
	}
	store, billing := report.Services[0], report.Services[1]
	if store.Namespace != "store" || store.SpanOverage != 2 || store.ByteOverage != expectedBytes-1 ||
		len(store.HighCardinality) != 1 || store.HighCardinality[0].Distinct != 2 ||
		billing.SpanOverage != 0 || billing.ByteOverage != 0 {
		t.Fatalf("services = %+v", report.Services)
	}
	data, err := json.Marshal(report)
	if err != nil || strings.Contains(string(data), "private-value") {
		t.Fatalf("budget JSON = %s, %v", data, err)
	}
	next := now.Add(time.Hour)
	if current := tracker.Snapshot(next, false); current.Total.Spans != 0 {
		t.Fatalf("next window = %+v", current)
	}
	if previous := tracker.Snapshot(next, true); previous.Total.Spans != 4 || !previous.Start.Equal(report.Start) {
		t.Fatalf("previous window = %+v", previous)
	}
}

func TestBudgetBoundsServiceAndAttributeTracking(t *testing.T) {
	now := time.Now()
	tracker := New(Config{Enabled: true, Attributes: []string{"id"}, CardinalityLimit: 255,
		Services: []ServiceLimit{{Service: "reserved", Limits: Limits{Spans: 1}}}}, now)
	for index := range maxServices + 1 {
		tracker.Record(now, []telemetry.Span{{ServiceName: fmt.Sprintf("service-%d", index)}},
			[]Observation{{Span: &trace.Span{}}})
	}
	for index := range maxDistinct + 8 {
		raw := &trace.Span{Attributes: []*common.KeyValue{{
			Key: "id", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: int64(index)}},
		}}}
		tracker.Record(now, []telemetry.Span{{ServiceName: "reserved"}}, []Observation{{Span: raw}})
	}
	report := tracker.Snapshot(now, false)
	if len(report.Services) != maxServices+1 || report.Other.Spans != 1 || report.Services[0].Name != "reserved" ||
		len(report.Services[0].HighCardinality) != 1 || !report.Services[0].HighCardinality[0].AtLeast ||
		report.Services[0].HighCardinality[0].Distinct != maxDistinct {
		t.Fatalf("bounded snapshot = %+v", report)
	}
}
