// A finite SDK fixture avoids the interactive Go example's fixed port 8080.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL") != "http/protobuf" {
		panic("this fixture requires http/protobuf")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		panic(err)
	}
	res, err := resource.New(ctx, resource.WithFromEnv())
	if err != nil {
		panic(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	// The second root span links to the first trace rather than adopting it as
	// its parent. These IDs come from actual SDK-created spans.
	var linked trace.SpanContext
	for _, scenario := range []string{"success", "failure"} {
		options := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
		if linked.IsValid() {
			options = append(options, trace.WithLinks(trace.Link{SpanContext: linked}))
		}
		_, span := provider.Tracer("geko.compatibility.go").Start(ctx, "GET /"+scenario, options...)
		status := 200
		if scenario == "failure" {
			status = 500
			span.RecordError(errors.New("synthetic compatibility failure"), trace.WithStackTrace(true))
			span.SetStatus(codes.Error, "synthetic failure")
			span.AddEvent("retry", trace.WithAttributes(attribute.Int64("retry.attempt", 9007199254740993), attribute.Float64("retry.delay_ms", 1.0)))
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		span.End()
		record := map[string]any{"scenario": scenario, "status": status, "trace_id": span.SpanContext().TraceID().String(), "span_id": span.SpanContext().SpanID().String()}
		if linked.IsValid() {
			record["linked_trace_id"], record["linked_span_id"] = linked.TraceID().String(), linked.SpanID().String()
		}
		if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
			panic(err)
		}
		linked = span.SpanContext()
	}
	if err := provider.Shutdown(ctx); err != nil {
		panic(err)
	}
}
