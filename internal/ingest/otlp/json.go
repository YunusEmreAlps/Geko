package otlp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type jsonObject map[string]json.RawMessage

// OTLP JSON encodes trace and span IDs as hexadecimal. Other protobuf byte
// fields still use base64, so translate only the IDs on spans and links.
func decodeJSON(payload []byte, request *collector.ExportTraceServiceRequest) error {
	if err := checkJSONBatch(bytes.NewReader(payload)); err != nil {
		return err
	}
	var root jsonObject
	if err := json.Unmarshal(payload, &root); err != nil {
		return err
	}
	var resources, scopes, spans int
	err := rewriteObjects(root, "resourceSpans", "resource_spans", func(resource jsonObject) error {
		resources++
		if resources > maxSpans {
			return errBatchLimit
		}
		return rewriteObjects(resource, "scopeSpans", "scope_spans", func(scope jsonObject) error {
			scopes++
			if scopes > maxSpans {
				return errBatchLimit
			}
			return rewriteObjects(scope, "spans", "", func(span jsonObject) error {
				spans++
				if spans > maxSpans {
					return errBatchLimit
				}
				if err := rewriteIDs(span, "traceId", "trace_id", "spanId", "span_id", "parentSpanId", "parent_span_id"); err != nil {
					return err
				}
				return rewriteObjects(span, "links", "", func(link jsonObject) error {
					return rewriteIDs(link, "traceId", "trace_id", "spanId", "span_id")
				})
			})
		})
	})
	if err != nil {
		return err
	}
	wire, err := json.Marshal(root)
	if err != nil {
		return err
	}
	return (protojson.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 32}).Unmarshal(wire, request)
}

func rewriteObjects(object jsonObject, field, alias string, rewrite func(jsonObject) error) error {
	for _, key := range []string{field, alias} {
		value, ok := object[key]
		if key == "" || !ok {
			continue
		}
		var items []jsonObject
		if err := json.Unmarshal(value, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := rewrite(item); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return err
		}
		object[key] = encoded
	}
	return nil
}

func rewriteIDs(object jsonObject, fields ...string) error {
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			continue
		}
		var id string
		if err := json.Unmarshal(value, &id); err != nil {
			return err
		}
		want := 16
		if strings.EqualFold(field, "traceId") || field == "trace_id" {
			want = 32
		}
		if id == "" && (field == "parentSpanId" || field == "parent_span_id") {
			continue
		}
		// Map malformed IDs to one byte so validation rejects the span even
		// for an optional parent ID. Preserve the rest of the export.
		decoded := []byte{0}
		if len(id) == want {
			if bytes, err := hex.DecodeString(id); err == nil {
				decoded = bytes
			}
		}
		object[field], _ = json.Marshal(decoded)
	}
	return nil
}
