package otlp

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	oteltrace "go.opentelemetry.io/otel/trace"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

const maxLinks = 128
const maxRetainedAttributes = 64

var eventFields = map[string]int{
	"retry.attempt": labelSize, "retry.count": labelSize, "retry.delay_ms": labelSize,
	"error.type": labelSize, "event.name": labelSize, "event.domain": labelSize,
	"messaging.operation.type": labelSize,
}

func retentionPolicy(policies []privacy.AttributeRetention) privacy.AttributeRetention {
	if len(policies) != 0 {
		return policies[0]
	}
	return privacy.AttributeRetention{}
}

// retainedAttributes is the single bounded scalar conversion for extra fields.
// Arrays and objects are deliberately omitted rather than flattened or stringified.
func retainedAttributes(src []*common.KeyValue, keys []string, defaults map[string]int, strict bool, search privacy.AttributeSearch, loss *telemetry.Filtering) telemetry.Attributes {
	attrs := make(telemetry.Attributes)
	for _, attr := range src {
		key := attr.GetKey()
		limit, allowed := defaults[key]
		if !allowed && !strict && !privacy.SensitiveAttribute(key) && !search.Protects(key) {
			for _, candidate := range keys {
				if key == candidate {
					allowed = true
					limit = 1024
					break
				}
			}
		}
		if !allowed || len(attrs) >= maxRetainedAttributes {
			loss.Attributes++
			continue
		}
		switch value := attr.GetValue().GetValue().(type) {
		case *common.AnyValue_StringValue:
			if len(value.StringValue) > limit {
				loss.TruncatedValues++
			}
			attrs[key] = privacy.Text(value.StringValue, limit)
		case *common.AnyValue_IntValue:
			attrs[key] = value.IntValue
		case *common.AnyValue_BoolValue:
			attrs[key] = value.BoolValue
		case *common.AnyValue_DoubleValue:
			if math.IsNaN(value.DoubleValue) || math.IsInf(value.DoubleValue, 0) {
				loss.Attributes++
				continue
			}
			number := strconv.FormatFloat(value.DoubleValue, 'g', -1, 64)
			if !strings.ContainsAny(number, ".eE") {
				number += ".0"
			}
			attrs[key] = json.Number(number)
		default:
			loss.Attributes++
		}
	}
	return attrs
}

func filteredTraceState(value string, strict bool, loss *telemetry.Filtering) string {
	if value == "" {
		return ""
	}
	// Validate the W3C list using the official API, then apply our privacy boundary.
	if strict || len(value) > 512 || privacy.Text(value, 512) != value {
		loss.Metadata++
		return ""
	}
	if _, err := oteltrace.ParseTraceState(value); err != nil {
		loss.Metadata++
		return ""
	}
	return value
}

func schemaURL(value string, loss *telemetry.Filtering) string {
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || len(value) > 1024 || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || privacy.Text(value, 1024) != value {
		loss.Metadata++
		return ""
	}
	return value
}

func normalizeMetadata(src *trace.Span, strict bool, policy privacy.AttributeRetention) telemetry.Metadata {
	m := telemetry.Metadata{Present: true, Flags: src.GetFlags(), DroppedAttributesCount: src.GetDroppedAttributesCount(), DroppedEventsCount: src.GetDroppedEventsCount(), DroppedLinksCount: src.GetDroppedLinksCount()}
	m.TraceState = filteredTraceState(src.GetTraceState(), strict, &m.Filtering)
	for i, link := range src.GetLinks() {
		if i >= maxLinks || len(link.GetTraceId()) != 16 || len(link.GetSpanId()) != 8 || allZero(link.GetTraceId()) || allZero(link.GetSpanId()) {
			m.Filtering.Links++
			continue
		}
		m.Links = append(m.Links, telemetry.Link{TraceID: hex.EncodeToString(link.GetTraceId()), SpanID: hex.EncodeToString(link.GetSpanId()), Flags: link.GetFlags(), TraceState: filteredTraceState(link.GetTraceState(), strict, &m.Filtering), DroppedAttributesCount: link.GetDroppedAttributesCount(), Attributes: retainedAttributes(link.GetAttributes(), policy.Link, nil, strict, privacy.AttributeSearch{}, &m.Filtering)})
	}
	return m
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func accountAttributes(src []*common.KeyValue, stored telemetry.Attributes, limits map[string]int, loss *telemetry.Filtering, fallbacks ...map[string]int) {
	for _, attr := range src {
		key := attr.GetKey()
		if isProvenanceKey(key) {
			continue
		}
		if key == "http.method" {
			key = "http.request.method"
		}
		if key == "http.status_code" {
			key = "http.response.status_code"
		}
		_, ok := stored[key]
		if !ok {
			_, ok = stored[privacy.AttributeDigestPrefix+key]
		}
		if !ok && attr.GetKey() == "exception.message" {
			_, ok = stored["exception.message_hash"]
		}
		if !ok {
			loss.Attributes++
			continue
		}
		if v, ok := attr.GetValue().GetValue().(*common.AnyValue_StringValue); ok {
			limit := limits[attr.GetKey()]
			for _, fallback := range fallbacks {
				if limit == 0 {
					limit = fallback[attr.GetKey()]
				}
			}
			if limit == 0 {
				limit = 1024
			}
			if limit > 0 && len(v.StringValue) > limit {
				loss.TruncatedValues++
			}
		}
	}
}

func mergeExtra(src []*common.KeyValue, attrs telemetry.Attributes, keys []string, strict bool, search privacy.AttributeSearch) {
	if strict || len(keys) == 0 {
		return
	}
	var ignored telemetry.Filtering
	extra := retainedAttributes(src, keys, nil, false, search, &ignored)
	for key, value := range extra {
		if _, exists := attrs[key]; !exists && !strings.HasPrefix(key, "geko.") {
			attrs[key] = value
		}
	}
}
