package otlp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const TransferBytes = maxPayloadBytes

var ErrTransferLimit = errors.New("OTLP JSON exceeds the 4 MiB or 2,048 span transfer limit")
var ErrInvalidTransfer = errors.New("expected valid OTLP JSON containing spans from one trace")
var ErrTransferBusy = errors.New("trace receiver is busy")
var ErrTraceExists = errors.New("the trace is already stored; restoring it would merge spans into the stored trace")

// EncodeTraceJSON produces one bounded OTLP request. Each stored span carries
// its own resource and scope, preserving traces across service boundaries.
func EncodeTraceJSON(id telemetry.TraceID, strict bool, visit func(func(investigation.Span) error) error) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(`{"resourceSpans":[`)
	count := 0
	err := visit(func(span investigation.Span) error {
		if count == maxSpans {
			return ErrTransferLimit
		}
		item, err := encodeTransferSpan(id, span, strict)
		if err != nil {
			return err
		}
		if output.Len()+len(item)+3 > maxPayloadBytes {
			return ErrTransferLimit
		}
		if count != 0 {
			output.WriteByte(',')
		}
		output.Write(item)
		count++
		return nil
	})
	if err != nil {
		return nil, err
	}
	output.WriteString("]}")
	if err := checkJSONBatch(bytes.NewReader(output.Bytes())); err != nil {
		return nil, ErrTransferLimit
	}
	return output.Bytes(), nil
}

func encodeTransferSpan(id telemetry.TraceID, span investigation.Span, strict bool) ([]byte, error) {
	spanID, err := hex.DecodeString(span.SpanID)
	if err != nil {
		return nil, err
	}
	parent, err := hex.DecodeString(span.ParentID)
	if err != nil {
		return nil, err
	}
	wire := &trace.Span{
		TraceId: id[:], SpanId: spanID, ParentSpanId: parent, Name: span.Operation,
		Kind: trace.Span_SpanKind(span.Kind), StartTimeUnixNano: uint64(span.Start.UnixNano()), EndTimeUnixNano: uint64(span.End.UnixNano()),
		Status:     &trace.Status{Code: trace.Status_StatusCode(span.StatusCode), Message: span.StatusDescription},
		Attributes: transferAttributes(span.Attributes),
	}
	for _, event := range span.Events {
		wire.Events = append(wire.Events, &trace.Span_Event{
			Name: event.Name, TimeUnixNano: uint64(event.Time.UnixNano()), Attributes: transferAttributes(event.Attributes),
		})
	}
	// Apply the current policy to records written under a previous policy.
	normalized, err := normalizeSpan(wire, span.Service, nil, telemetry.Scope{}, strict)
	if err != nil {
		return nil, ErrInvalidTransfer
	}
	wire.Name = normalized.Name
	wire.Status.Message = normalized.StatusDescription
	wire.Attributes = transferAttributes(normalized.Attributes)
	for i, event := range normalized.Events {
		wire.Events[i].Name = event.Name
		wire.Events[i].Attributes = transferAttributes(event.Attributes)
	}
	body, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(wire)
	if err != nil {
		return nil, err
	}
	var object jsonObject
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	object["traceId"], _ = json.Marshal(hex.EncodeToString(id[:]))
	object["spanId"], _ = json.Marshal(span.SpanID)
	if span.ParentID != "" {
		object["parentSpanId"], _ = json.Marshal(span.ParentID)
	}
	resourceAttrs := maps.Clone(span.ResourceAttributes)
	if resourceAttrs == nil {
		resourceAttrs = make(telemetry.Attributes)
	}
	resourceAttrs["service.name"] = span.Service
	resourceJSON, err := protojson.Marshal(&resource.Resource{Attributes: transferAttributes(textAttributes(transferAttributes(resourceAttrs), resourceFields))})
	if err != nil {
		return nil, err
	}
	scopeAttrs := textAttributes(transferAttributes(span.Scope.Attributes), scopeFields)
	scopeJSON, err := protojson.Marshal(&common.InstrumentationScope{
		Name: privacy.Text(span.Scope.Name, labelSize), Version: privacy.Text(span.Scope.Version, labelSize), Attributes: transferAttributes(scopeAttrs),
	})
	if err != nil {
		return nil, err
	}
	type scopeSpans struct {
		Scope json.RawMessage `json:"scope"`
		Spans []jsonObject    `json:"spans"`
	}
	return json.Marshal(struct {
		Resource json.RawMessage `json:"resource"`
		Scopes   []scopeSpans    `json:"scopeSpans"`
	}{resourceJSON, []scopeSpans{{scopeJSON, []jsonObject{object}}}})
}

func transferAttributes(attrs telemetry.Attributes) []*common.KeyValue {
	var result []*common.KeyValue
	for _, key := range slices.Sorted(maps.Keys(attrs)) {
		value := &common.AnyValue{}
		switch v := attrs[key].(type) {
		case string:
			value.Value = &common.AnyValue_StringValue{StringValue: v}
		case int64:
			value.Value = &common.AnyValue_IntValue{IntValue: v}
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				continue
			}
			value.Value = &common.AnyValue_IntValue{IntValue: n}
		default:
			continue
		}
		result = append(result, &common.KeyValue{Key: key, Value: value})
	}
	return result
}

// RestoreTraceJSON validates the entire file before a single storage write. It
// refuses a trace that is already stored so a file cannot add spans to it.
func (h *Receiver) RestoreTraceJSON(ctx context.Context, input io.Reader, stored func(context.Context, telemetry.TraceID) (bool, error)) (telemetry.TraceID, error) {
	var id telemetry.TraceID
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return id, ErrTransferBusy
	}
	payload, err := io.ReadAll(io.LimitReader(input, maxPayloadBytes+1))
	if err != nil {
		return id, ErrInvalidTransfer
	}
	if len(payload) > maxPayloadBytes {
		return id, ErrTransferLimit
	}
	var request collector.ExportTraceServiceRequest
	if err := decodeJSON(payload, &request); err != nil {
		if errors.Is(err, errBatchLimit) {
			return id, ErrTransferLimit
		}
		return id, ErrInvalidTransfer
	}
	spans, rejected, err := normalize(&request, h.strict)
	if err != nil || rejected != 0 || len(spans) == 0 {
		return id, ErrInvalidTransfer
	}
	id = spans[0].TraceID
	seen := make(map[telemetry.SpanID]struct{}, len(spans))
	for _, span := range spans {
		if span.TraceID != id {
			return telemetry.TraceID{}, ErrInvalidTransfer
		}
		if _, exists := seen[span.ID]; exists {
			return telemetry.TraceID{}, ErrInvalidTransfer
		}
		seen[span.ID] = struct{}{}
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if exists, err := stored(ctx, id); err != nil {
		return id, err
	} else if exists {
		return id, ErrTraceExists
	}
	return id, h.store.WriteSpans(ctx, time.Now(), spans)
}
