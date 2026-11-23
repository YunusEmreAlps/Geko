package web

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestConnectDistinguishesSDKObservationFromMissingData(t *testing.T) {
	for _, scenario := range []string{"received", "wrong-token", "wrong-endpoint", "missing-run-id", "idle"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "connect-sdk.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var raw [32]byte
			rand.Read(raw[:])
			secret := hex.EncodeToString(raw[:])
			token, err := access.ParseToken(secret)
			if err != nil {
				t.Fatal(err)
			}
			receiver := otlp.NewHandler(store)
			mux := http.NewServeMux()
			mux.Handle("POST /v1/traces", receiver)
			if err := Register(mux, store, Settings{ListenAddr: "127.0.0.1:4318", AccessToken: true, Tokens: token,
				ReceiverConnections: receiver.Connections, ReceiverDiagnostics: receiver.Diagnostics}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(token.Protect(mux))
			defer server.Close()
			since := strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)
			if scenario != "idle" {
				endpoint, credential := server.URL+"/v1/traces", secret
				if scenario == "wrong-endpoint" {
					endpoint = server.URL + "/wrong/v1/traces"
				}
				if scenario == "wrong-token" {
					credential = "incorrect"
				}
				exporter, err := otlptracehttp.New(t.Context(), otlptracehttp.WithEndpointURL(endpoint),
					otlptracehttp.WithHeaders(map[string]string{"authorization": "Bearer " + credential}),
					otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}), otlptracehttp.WithTimeout(time.Second))
				if err != nil {
					t.Fatal(err)
				}
				attrs := []attribute.KeyValue{attribute.String("service.name", "api"), attribute.String("service.namespace", "Shop")}
				if scenario != "missing-run-id" {
					attrs = append(attrs, attribute.String("geko.run.id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
				}
				provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithResource(resource.NewSchemaless(attrs...)))
				_, span := provider.Tracer("connect-test").Start(t.Context(), "request")
				span.End()
				if err := provider.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			req, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/connect?service=api&application=Shop&since="+since, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+secret)
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("Connect status %d, %v", response.StatusCode, err)
			}
			observed := scenario == "received" || scenario == "missing-run-id"
			if strings.Contains(string(body), "Telemetry observed for") != observed {
				t.Fatalf("%s reported the wrong connection state", scenario)
			}
			if !observed && !strings.Contains(string(body), "Waiting for telemetry") {
				t.Fatal("missing data was not explicit")
			}
			if scenario == "missing-run-id" && !strings.Contains(string(body), "needs the SDK to export") {
				t.Fatal("missing run ID was not disclosed")
			}
			if strings.Contains(string(body), secret) {
				t.Fatal("Connect exposed credentials")
			}
		})
	}
}
