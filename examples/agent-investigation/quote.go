// Package agentinvestigation is a deliberate, synthetic investigation fixture.
package agentinvestigation

import (
	"context"
	"runtime"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Quote accepts surrounding whitespace in the quantity field.
func Quote(ctx context.Context, tracer trace.Tracer, quantity string, unitPrice int) (int, error) {
	_, span := tracer.Start(ctx, "quote", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	_, file, line, _ := runtime.Caller(0)
	count, err := strconv.Atoi(strings.TrimSpace(quantity))
	span.SetAttributes(attribute.String("code.file.path", file), attribute.Int("code.line.number", line+1), attribute.String("code.function.name", "Quote"))
	if err != nil {
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}
	return count * unitPrice, nil
}
