package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestConnectPage(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "connect.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page := func(settings Settings, query string, status int) string {
		t.Helper()
		mux := http.NewServeMux()
		if err := Register(mux, store, settings); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4318/connect"+query, nil))
		if w.Code != status {
			t.Fatalf("%s returned %d: %s", query, w.Code, w.Body)
		}
		return html.UnescapeString(w.Body.String())
	}

	local := page(Settings{ListenAddr: "127.0.0.1:4318"}, "?service=checkout&application=Storefront", http.StatusOK)
	for _, fragment := range []string{
		"export OTEL_SERVICE_NAME=checkout", "export OTEL_RESOURCE_ATTRIBUTES=service.namespace=Storefront",
		"export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\n", "$env:OTEL_LOGS_EXPORTER = 'none'",
		"  OTEL_METRICS_EXPORTER: none", `href="/traces?service=checkout"`, "OTLP/gRPC (disabled)",
	} {
		if !strings.Contains(local, fragment) {
			t.Errorf("local page missing %q", fragment)
		}
	}
	if strings.Contains(local, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Error("local page without a token sets an authorization header")
	}

	remote := Settings{ListenAddr: "0.0.0.0:4318", PublicURL: "https://geko.example.com", AccessToken: true, IngestTokens: []string{"storefront", "reporting"}}
	body := page(remote, "?service=checkout&protocol=grpc&token=reporting", http.StatusOK)
	for _, fragment := range []string{
		"export OTEL_EXPORTER_OTLP_ENDPOINT=https://geko.example.com\n", "export OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
		`export OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer%20${GEKO_INGEST_TOKEN}"`,
		`$env:OTEL_EXPORTER_OTLP_HEADERS = "authorization=Bearer%20$env:GEKO_INGEST_TOKEN"`,
		"the <code>reporting</code> token", `<option value="reporting" selected>`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("remote page missing %q", fragment)
		}
	}

	page(Settings{ListenAddr: "127.0.0.1:4318"}, "?service=checkout&protocol=grpc", http.StatusBadRequest)
	page(Settings{ListenAddr: "127.0.0.1:4318"}, "?service=bad%22name", http.StatusBadRequest)
	page(Settings{ListenAddr: "127.0.0.1:4318"}, "?service=ok&application=a%20b", http.StatusBadRequest)
	page(remote, "?service=checkout&token=unknown", http.StatusBadRequest)
	page(Settings{ListenAddr: "127.0.0.1:4318", GRPCAddr: "127.0.0.1:4317"}, "?service=checkout&protocol=grpc", http.StatusOK)
}
