package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Exercise the real SDK, OTLP HTTP receiver, persistence and evidence queries.
func TestScenariosThroughGeko(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "geko.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/traces", otlp.NewHandler(store))
	receiver := httptest.NewServer(mux)
	defer receiver.Close()
	exporter, err := otlptracehttp.New(t.Context(), otlptracehttp.WithEndpointURL(receiver.URL+"/v1/traces"))
	if err != nil {
		t.Fatal(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "geko-go-checkout"))))
	handler := newHandler(provider.Tracer("github.com/YunusEmreAlps/geko/examples/go"))
	ids := make(map[string]string)
	for _, scenario := range []string{"success", "failure", "retry"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/"+scenario, nil))
		want := 200
		if scenario == "failure" {
			want = 500
		}
		if response.Code != want {
			t.Fatalf("%s: got %d, want %d", scenario, response.Code, want)
		}
		ids[scenario] = response.Header().Get("X-Trace-ID")
		if len(ids[scenario]) != 32 {
			t.Fatalf("missing trace ID for %s", scenario)
		}
	}
	// Shutdown must drain the batch before querying storage.
	if err := provider.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err := store.Failures(t.Context(), investigation.FailureFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d failures, want 2", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Evidence == nil || item.Evidence.ExceptionMessage != "demo payment gateway unavailable" {
			t.Fatalf("missing exception evidence: %+v", item)
		}
		if item.EntrySpan == nil || item.EntrySpan.HTTPStatus == nil {
			t.Fatal("missing request outcome")
		}
		switch item.TraceID {
		case ids["failure"]:
			if *item.EntrySpan.HTTPStatus != 500 || item.HTTPOutcome != "server_error" {
				t.Fatalf("incorrect failure outcome: %+v", item)
			}
		case ids["retry"]:
			if *item.EntrySpan.HTTPStatus != 200 || item.HTTPOutcome != "non_5xx" {
				t.Fatalf("retry must preserve HTTP 200: %+v", item)
			}
		default:
			t.Fatalf("unexpected failure trace %s", item.TraceID)
		}
	}
}
