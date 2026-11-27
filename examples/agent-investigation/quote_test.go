package agentinvestigation

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The same workload is used before and after the scratch-source change.
func TestQuote(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exporter, err := otlptracehttp.New(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		res, err := resource.New(t.Context(), resource.WithAttributes(
			attribute.String("service.name", "agent-quote-fixture"),
			attribute.String("deployment.environment.name", "synthetic-test"),
			attribute.String("service.version", "fixture-v1")), resource.WithFromEnv())
		if err != nil {
			t.Fatal(err)
		}
		provider = sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Errorf("flush telemetry: %v", err)
		}
	})
	for _, input := range []string{"2", " 2 "} {
		t.Run(input, func(t *testing.T) {
			got, err := Quote(t.Context(), provider.Tracer("synthetic-quote"), input, 500)
			if err != nil || got != 1000 {
				t.Errorf("Quote(%q, 500) = %d, %v; want 1000, nil", input, got, err)
			}
		})
	}
}
