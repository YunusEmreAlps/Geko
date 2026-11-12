package otlp

import (
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Bound all decoded messages, including KeyValue, AnyValue, events and links.
// A byte limit alone cannot bound allocations for empty messages. The budget
// scales with the span limit so a full batch of typically attributed spans
// (each attribute is a KeyValue and an AnyValue) still fits.
const maxDecodedMessages = maxSpans * 64
const maxWireDepth = 32

func checkWireBatch(wire []byte) error {
	remaining := maxDecodedMessages
	envelopes := make(map[protoreflect.FullName]int)
	var walk func([]byte, protoreflect.MessageDescriptor, int) error
	walk = func(wire []byte, descriptor protoreflect.MessageDescriptor, depth int) error {
		if depth > maxWireDepth {
			return errBatchLimit
		}
		for len(wire) > 0 {
			number, kind, n := protowire.ConsumeTag(wire)
			if n < 0 {
				return protowire.ParseError(n)
			}
			wire = wire[n:]
			field := descriptor.Fields().ByNumber(number)
			if field != nil && field.Message() != nil && kind == protowire.BytesType {
				message, size := protowire.ConsumeBytes(wire)
				if size < 0 {
					return protowire.ParseError(size)
				}
				remaining--
				if remaining < 0 {
					return errBatchLimit
				}
				child := field.Message()
				switch child.FullName() {
				case "opentelemetry.proto.trace.v1.ResourceSpans", "opentelemetry.proto.trace.v1.ScopeSpans", "opentelemetry.proto.trace.v1.Span":
					envelopes[child.FullName()]++
					if envelopes[child.FullName()] > maxSpans {
						return errBatchLimit
					}
				}
				if err := walk(message, child, depth+1); err != nil {
					return err
				}
				n = size
			} else {
				n = protowire.ConsumeFieldValue(number, kind, wire)
				if n < 0 {
					return protowire.ParseError(n)
				}
			}
			wire = wire[n:]
		}
		return nil
	}
	return walk(wire, (&collector.ExportTraceServiceRequest{}).ProtoReflect().Descriptor(), 0)
}
