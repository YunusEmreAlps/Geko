package otlp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"

	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
)

func envelope(field protowire.Number, data []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), data)
}

func TestWireNestedMessageBudgets(t *testing.T) {
	empty := func(field protowire.Number) []byte { return bytes.Repeat(envelope(field, nil), maxDecodedMessages+1) }
	cases := map[string][]byte{
		"resource attributes": envelope(1, envelope(1, empty(1))),
		"scope attributes":    envelope(1, envelope(2, envelope(1, empty(3)))),
	}
	for name, field := range map[string]protowire.Number{"span attributes": 9, "events": 11, "links": 13} {
		cases[name] = envelope(1, envelope(2, envelope(2, empty(field))))
	}
	cases["event attributes"] = envelope(1, envelope(2, envelope(2, envelope(11, empty(3)))))
	cases["link attributes"] = envelope(1, envelope(2, envelope(2, envelope(13, empty(4)))))
	// Resource attribute -> KeyValue.value -> AnyValue.array_value -> values.
	cases["nested values"] = envelope(1, envelope(1, envelope(1, envelope(2, envelope(5, empty(1))))))
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			if err := checkWireBatch(wire); !errors.Is(err, errBatchLimit) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if err := checkWireBatch(envelope(1, envelope(1, envelope(1, nil)))); err != nil {
		t.Fatal(err)
	}
}

func TestJSONStreamingLimits(t *testing.T) {
	for _, name := range []string{"resourceSpans", "resource_spans", "scopeSpans", "scope_spans", "spans", "attributes", "events", "links", "values"} {
		t.Run(name, func(t *testing.T) {
			payload := `{"` + name + `":[` + strings.Repeat(`{},`, maxDecodedMessages) + `{}]}`
			if err := checkJSONBatch(strings.NewReader(payload)); !errors.Is(err, errBatchLimit) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, payload := range []string{`{} {}`, `{"a":[}`, `{"a":`} {
		if err := checkJSONBatch(strings.NewReader(payload)); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	if err := checkJSONBatch(strings.NewReader(`{"resourceSpans":[{}]}`)); err != nil {
		t.Fatal(err)
	}
}

// A full batch of spans with typical attributes and an exception event must fit
// the nested message budgets in both encodings.
func TestFullBatchWithAttributesIsAccepted(t *testing.T) {
	const attributes = 20
	spans := make([]*trace.Span, maxSpans)
	var payload strings.Builder
	payload.WriteString(`{"resourceSpans":[{"scopeSpans":[{"spans":[`)
	for i := range spans {
		span := fixtureSpan()
		span.SpanId = binary.BigEndian.AppendUint64(nil, uint64(i+1))
		if i > 0 {
			payload.WriteString(",")
		}
		fmt.Fprintf(&payload, `{"traceId":"%x","spanId":"%x","name":"GET /orders","startTimeUnixNano":"100","endTimeUnixNano":"200","attributes":[`, span.TraceId, span.SpanId)
		for a := range attributes {
			key, value := fmt.Sprintf("app.attribute.%d", a), "value"
			span.Attributes = append(span.Attributes, &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}})
			if a > 0 {
				payload.WriteString(",")
			}
			fmt.Fprintf(&payload, `{"key":%q,"value":{"stringValue":%q}}`, key, value)
		}
		payload.WriteString(`],"events":[{"timeUnixNano":"150","name":"exception","attributes":[`)
		event := &trace.Span_Event{TimeUnixNano: 150, Name: "exception"}
		for a, key := range []string{"exception.type", "exception.message", "exception.stacktrace"} {
			event.Attributes = append(event.Attributes, &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "fixture"}}})
			if a > 0 {
				payload.WriteString(",")
			}
			fmt.Fprintf(&payload, `{"key":%q,"value":{"stringValue":"fixture"}}`, key)
		}
		payload.WriteString(`]}]}`)
		span.Events = []*trace.Span_Event{event}
		spans[i] = span
	}
	payload.WriteString(`]}]}]}`)
	for _, tc := range []struct {
		mediaType string
		payload   []byte
	}{
		{protobufContentType, encodeBatch(t, spans...)},
		{jsonContentType, []byte(payload.String())},
	} {
		t.Run(tc.mediaType, func(t *testing.T) {
			var stored int
			handler := NewHandler(writeFunc(func(_ context.Context, _ time.Time, spans []telemetry.Span) error {
				stored += len(spans)
				return nil
			}))
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/traces", bytes.NewReader(tc.payload))
			request.Header.Set("Content-Type", tc.mediaType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || stored != maxSpans {
				t.Fatalf("status = %d, stored = %d, body size = %d", response.Code, stored, len(tc.payload))
			}
		})
	}
}

func BenchmarkRejectAmplifiedPayload(b *testing.B) {
	payloads := map[string][]byte{
		"json":     []byte(`{"resourceSpans":[` + strings.Repeat(`{},`, (maxPayloadBytes-32)/3) + `{}]}`),
		"protobuf": envelope(1, envelope(1, bytes.Repeat(envelope(1, nil), (maxPayloadBytes-32)/2))),
	}
	for name, payload := range payloads {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			handler := NewHandler(nil) // A rejected payload must never reach storage.
			for b.Loop() {
				request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/traces", bytes.NewReader(payload))
				if name == "json" {
					request.Header.Set("Content-Type", jsonContentType)
				} else {
					request.Header.Set("Content-Type", protobufContentType)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusRequestEntityTooLarge {
					b.Fatalf("status = %d", response.Code)
				}
			}
		})
	}
}
