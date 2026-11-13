package otlp

import (
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	maxSpans  = 2048
	maxEvents = 32
	labelSize = 256
)

var errBatchLimit = errors.New("trace batch exceeds a resource, scope, span, or event limit")

func normalize(req *collector.ExportTraceServiceRequest, strict bool) ([]telemetry.Span, int64, error) {
	var spans []telemetry.Span
	var rejected int64
	var firstError error
	var total int
	for _, resourceSpans := range req.GetResourceSpans() {
		resource := textAttributes(resourceSpans.GetResource().GetAttributes(), resourceFields)
		service, _ := resource["service.name"].(string)
		if service == "" {
			service = "unknown_service"
		}
		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			if len(scopeSpans.GetSpans()) > maxSpans-total {
				return nil, 0, errBatchLimit
			}
			total += len(scopeSpans.GetSpans())
			src := scopeSpans.GetScope()
			scope := telemetry.Scope{
				Name:       privacy.Text(src.GetName(), labelSize),
				Version:    privacy.Text(src.GetVersion(), labelSize),
				Attributes: textAttributes(src.GetAttributes(), scopeFields),
			}
			for _, span := range scopeSpans.GetSpans() {
				normalized, err := normalizeSpan(span, service, resource, scope, strict)
				if err != nil {
					rejected++
					if firstError == nil {
						firstError = err
					}
					continue
				}
				spans = append(spans, normalized)
			}
		}
	}
	if len(spans) == 0 && rejected > 0 {
		return nil, rejected, firstError
	}
	return spans, rejected, nil
}

func normalizeSpan(src *trace.Span, service string, resource telemetry.Attributes, scope telemetry.Scope, strict bool) (telemetry.Span, error) {
	var span telemetry.Span
	if len(src.GetTraceId()) != len(span.TraceID) || len(src.GetSpanId()) != len(span.ID) {
		return span, errors.New("invalid trace or span ID length")
	}
	copy(span.TraceID[:], src.GetTraceId())
	copy(span.ID[:], src.GetSpanId())
	if span.TraceID == (telemetry.TraceID{}) || span.ID == (telemetry.SpanID{}) {
		return span, errors.New("trace and span IDs must be nonzero")
	}
	if parent := src.GetParentSpanId(); len(parent) != 0 {
		if len(parent) != len(span.ParentID) {
			return span, errors.New("invalid parent span ID length")
		}
		copy(span.ParentID[:], parent)
		if span.ParentID == (telemetry.SpanID{}) || span.ParentID == span.ID {
			return span, errors.New("invalid parent span ID")
		}
	}
	if !validTime(src.GetStartTimeUnixNano()) || !validTime(src.GetEndTimeUnixNano()) {
		return span, errors.New("invalid span timestamp")
	}
	if len(src.GetEvents()) > maxEvents {
		return span, errBatchLimit
	}

	span.ServiceName = service
	span.Name = privacy.Text(src.GetName(), labelSize)
	if span.Name == "" {
		return span, errors.New("span name is required")
	}
	if kind := src.GetKind(); kind >= trace.Span_SPAN_KIND_INTERNAL && kind <= trace.Span_SPAN_KIND_CONSUMER {
		span.Kind = telemetry.SpanKind(kind)
	}
	span.StartTime = fromUnixNano(src.GetStartTimeUnixNano())
	span.EndTime = fromUnixNano(src.GetEndTimeUnixNano())
	switch src.GetStatus().GetCode() {
	case trace.Status_STATUS_CODE_OK:
		span.StatusCode = telemetry.StatusOK
	case trace.Status_STATUS_CODE_ERROR:
		span.StatusCode = telemetry.StatusError
	}
	if !strict {
		span.StatusDescription = privacy.Text(src.GetStatus().GetMessage(), 1024)
	}
	span.ResourceAttributes = resource
	span.Scope = scope
	span.Attributes = spanAttributes(src.GetAttributes(), strict)
	span.Events = make([]telemetry.Event, 0, len(src.GetEvents()))
	for _, event := range src.GetEvents() {
		if !validTime(event.GetTimeUnixNano()) {
			return span, errors.New("invalid span event timestamp")
		}
		attributes := telemetry.Attributes{}
		if event.GetName() == "exception" {
			if strict {
				attributes = strictException(event.GetAttributes())
			} else {
				attributes = exceptionAttributes(event.GetAttributes())
			}
		}
		span.Events = append(span.Events, telemetry.Event{
			Name:       privacy.Text(event.GetName(), labelSize),
			Time:       fromUnixNano(event.GetTimeUnixNano()),
			Attributes: attributes,
		})
	}
	return span, nil
}

func validTime(ns uint64) bool {
	return ns > 0 && ns <= math.MaxInt64
}

func fromUnixNano(ns uint64) time.Time {
	return time.Unix(0, int64(ns)).UTC()
}

var resourceFields = map[string]int{
	"service.name": labelSize, "service.namespace": labelSize, "service.version": labelSize, "service.instance.id": labelSize,
	"deployment.environment.name": labelSize, "deployment.environment": labelSize,
	"telemetry.sdk.name": labelSize, "telemetry.sdk.language": labelSize, "telemetry.sdk.version": labelSize,
}

var scopeFields = map[string]int{
	"otel.scope.name": labelSize, "otel.scope.version": labelSize, "code.namespace": labelSize,
}

var exceptionFields = map[string]int{
	"exception.type": labelSize, "exception.message": 1024, "exception.stacktrace": 32 << 10,
}

var spanFields = map[string]int{
	"http.request.method": labelSize, "http.method": labelSize, "http.route": labelSize,
	"error.type": labelSize, "network.protocol.version": labelSize,
}

var localSpanFields = map[string]int{
	"url.path":  1024,
	"db.system": labelSize, "db.system.name": labelSize, "db.namespace": labelSize,
	"db.operation": labelSize, "db.operation.name": labelSize, "db.collection.name": labelSize,
	"rpc.system": labelSize, "rpc.system.name": labelSize, "rpc.service": labelSize, "rpc.method": labelSize,
}

func strictException(src []*common.KeyValue) telemetry.Attributes {
	attrs := textAttributes(src, map[string]int{"exception.type": labelSize})
	preserveExceptionHash(src, attrs)
	for _, attr := range src {
		value, ok := attr.GetValue().GetValue().(*common.AnyValue_StringValue)
		if !ok {
			continue
		}
		switch attr.GetKey() {
		case "exception.message":
			attrs["exception.message_hash"] = privacy.ExceptionMessageHash(value.StringValue)
		}
	}
	return attrs
}

func exceptionAttributes(src []*common.KeyValue) telemetry.Attributes {
	attrs := textAttributes(src, exceptionFields)
	if _, hasMessage := attrs["exception.message"]; !hasMessage {
		preserveExceptionHash(src, attrs)
	}
	return attrs
}

func preserveExceptionHash(src []*common.KeyValue, attrs telemetry.Attributes) {
	for _, attr := range src {
		if attr.GetKey() != "exception.message_hash" {
			continue
		}
		hash := attr.GetValue().GetStringValue()
		if len(hash) == 64 {
			if decoded, err := hex.DecodeString(hash); err == nil {
				attrs["exception.message_hash"] = hex.EncodeToString(decoded)
			}
		}
	}
}

func textAttributes(src []*common.KeyValue, allowlists ...map[string]int) telemetry.Attributes {
	attrs := make(telemetry.Attributes)
	for _, attr := range src {
		key := attr.GetKey()
		value, ok := attr.GetValue().GetValue().(*common.AnyValue_StringValue)
		if !ok {
			continue
		}
		for _, allowlist := range allowlists {
			if limit, ok := allowlist[key]; ok {
				attrs[key] = privacy.Text(value.StringValue, limit)
				break
			}
		}
	}
	return attrs
}

func spanAttributes(src []*common.KeyValue, strict bool) telemetry.Attributes {
	fields := []map[string]int{spanFields}
	if !strict {
		fields = append(fields, localSpanFields)
	}
	attrs := textAttributes(src, fields...)
	if _, exists := attrs["http.request.method"]; !exists {
		if legacy, exists := attrs["http.method"]; exists {
			attrs["http.request.method"] = legacy
		}
	}
	delete(attrs, "http.method")

	var status, legacyStatus int64
	for _, attr := range src {
		switch attr.GetKey() {
		case "http.response.status_code":
			status = attr.GetValue().GetIntValue()
		case "http.status_code":
			legacyStatus = attr.GetValue().GetIntValue()
		}
	}
	if status < 100 || status > 599 {
		status = legacyStatus
	}
	if status >= 100 && status <= 599 {
		attrs["http.response.status_code"] = status
	}
	return attrs
}
