package otlp

import (
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	common "go.opentelemetry.io/proto/otlp/common/v1"
)

const provenanceVersion = "geko.otel.provenance_version"
const capturedKey = "geko.otel.captured"

var provenanceCounts = [...]string{"geko.otel.filtered_attributes", "geko.otel.truncated_values", "geko.otel.filtered_links", "geko.otel.filtered_metadata"}

func isProvenanceKey(key string) bool {
	if key == provenanceVersion || key == capturedKey {
		return true
	}
	for _, candidate := range provenanceCounts {
		if key == candidate {
			return true
		}
	}
	return false
}

func provenanceAttributes(meta telemetry.Metadata) []*common.KeyValue {
	attrs := []*common.KeyValue{
		{Key: provenanceVersion, Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 1}}},
		{Key: capturedKey, Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: meta.Present}}},
	}
	counts := [...]uint64{meta.Filtering.Attributes, meta.Filtering.TruncatedValues, meta.Filtering.Links, meta.Filtering.Metadata}
	for i, count := range counts {
		if count != 0 {
			attrs = append(attrs, &common.KeyValue{Key: provenanceCounts[i], Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: int64(min(count, 1<<32))}}})
		}
	}
	return attrs
}

// Provenance is explicitly Geko-supplied, untrusted diagnostic metadata, never
// producer dropped counts or a completeness/authenticity assertion. Fixed keys,
// types and bounds prevent this extension from becoming a payload bypass.
func restoreProvenance(attrs []*common.KeyValue, meta *telemetry.Metadata) {
	var version bool
	var captured *bool
	var counts [4]uint64
	seen := make(map[string]bool)
	for _, attr := range attrs {
		key := attr.GetKey()
		if !isProvenanceKey(key) {
			continue
		}
		if seen[key] {
			return
		}
		seen[key] = true
		if key == capturedKey {
			v, ok := attr.GetValue().GetValue().(*common.AnyValue_BoolValue)
			if !ok {
				return
			}
			captured = &v.BoolValue
			continue
		}
		v, ok := attr.GetValue().GetValue().(*common.AnyValue_IntValue)
		if !ok || v.IntValue < 0 || v.IntValue > 1<<32 {
			return
		}
		if key == provenanceVersion {
			version = v.IntValue == 1
			continue
		}
		for i, candidate := range provenanceCounts {
			if key == candidate {
				counts[i] = uint64(v.IntValue)
			}
		}
	}
	if !version || captured == nil {
		return
	}
	meta.Present = *captured
	meta.Filtering.Attributes += counts[0]
	meta.Filtering.TruncatedValues += counts[1]
	meta.Filtering.Links += counts[2]
	meta.Filtering.Metadata += counts[3]
}

func addFiltering(target *telemetry.Filtering, source telemetry.Filtering) {
	target.Attributes += source.Attributes
	target.TruncatedValues += source.TruncatedValues
	target.Links += source.Links
	target.Metadata += source.Metadata
}
