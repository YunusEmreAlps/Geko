package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type captureTransport func(*http.Request) (*http.Response, error)

func (f captureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExampleExporterTransport(t *testing.T) {
	for _, tc := range []struct{ name, endpoint, traceEndpoint, insecure, want string }{
		{"local default", "", "", "", "http://127.0.0.1:4318/v1/traces"},
		{"configured base", "http://collector.example:55681/otel", "", "", "http://collector.example:55681/otel/v1/traces"},
		{"secure signal override", "http://collector.example", "https://secure.example/custom", "", "https://secure.example/custom"},
		{"explicit secure default", "", "", "false", "https://localhost:4318/v1/traces"},
		{"explicit insecure default", "", "", "true", "http://localhost:4318/v1/traces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_INSECURE", "OTEL_EXPORTER_OTLP_TRACES_INSECURE", "OTEL_EXPORTER_OTLP_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE", "OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_CLIENT_KEY", "OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY"} {
				t.Setenv(name, "")
			}
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.endpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", tc.traceEndpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_INSECURE", tc.insecure)
			var target string
			client := &http.Client{Transport: captureTransport(func(r *http.Request) (*http.Response, error) {
				target = r.URL.String()
				if n, err := io.Copy(io.Discard, r.Body); err != nil || n == 0 {
					t.Errorf("empty/failed OTLP request: %d %v", n, err)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}
			options := append(exampleExporterOptions(), otlptracehttp.WithHTTPClient(client))
			exporter, err := otlptracehttp.New(t.Context(), options...)
			if err != nil {
				t.Fatal(err)
			}
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			_, span := provider.Tracer("transport-test").Start(t.Context(), "checkout")
			span.End()
			if err := provider.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if target != tc.want {
				t.Fatalf("export destination %q, want %q", target, tc.want)
			}
		})
	}
}
