package web

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestDashboardInvestigation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dashboard.db")
	store, err := sqlite.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	configPath := filepath.Join(t.TempDir(), "custom.yaml")
	if err := Register(mux, store, Settings{
		ListenAddr: "127.0.0.1:4318", DBPath: dbPath, Version: "test",
		ConfigSource: config.Source{
			FilePath: configPath, DBPath: config.SettingSource{Kind: "env", Variable: "GEKO_DB_PATH"},
			ListenAddr: config.SettingSource{Kind: "yaml"}, OnFull: config.SettingSource{Kind: "default"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	get := func(path string, status int, contains ...string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		body := w.Body.String()
		if w.Code != status {
			t.Fatalf("%s returned %d, want %d: %s", path, w.Code, status, body)
		}
		for _, fragment := range contains {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s missing %q", path, fragment)
			}
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s lacks a content security policy", path)
		}
		return body
	}
	get("/", http.StatusSeeOther)
	get("/failures", http.StatusOK, "No traces received yet", "http://127.0.0.1:4318/v1/traces", "go run ./examples/go")
	get("/assets/dashboard.css", http.StatusOK, ".waterfall")
	get("/assets/dashboard.js", http.StatusOK, "clipboard.writeText")
	get("/assets/theme.js", http.StatusOK, "geko.theme")
	get("/settings", http.StatusOK, "schema 10", "SQLite sidecars", "127.0.0.1:4318", configPath,
		"Set by <code>GEKO_DB_PATH</code>", ">yaml</span>", ">default</span>", "Traces evicted at capacity")

	start := time.Now().UTC().Add(-time.Minute)
	if err := store.WriteSpans(t.Context(), start, []telemetry.Span{{
		TraceID: telemetry.TraceID{2}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "healthy", Name: "GET /health", StartTime: start, EndTime: start.Add(time.Millisecond),
		Attributes: telemetry.Attributes{"http.response.status_code": 200},
	}}); err != nil {
		t.Fatal(err)
	}
	get("/failures", http.StatusOK, "No failure evidence found")
	get("/traces", http.StatusOK, "All traces", "GET /health", "HTTP 200", "None recorded")
	get("/traces?service=healthy&status=200", http.StatusOK, "GET /health")
	get("/traces?q=missing", http.StatusOK, "No matching traces")
	get("/traces?trace_id=", http.StatusBadRequest, "Invalid trace ID")
	successID := "02" + strings.Repeat("0", 30)
	lookup := httptest.NewRecorder()
	mux.ServeHTTP(lookup, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/traces?trace_id="+successID, nil))
	if location := lookup.Header().Get("Location"); lookup.Code != http.StatusSeeOther || location != "/traces/"+successID {
		t.Fatalf("successful trace lookup: %d %q", lookup.Code, location)
	}
	get("/traces/"+successID, http.StatusOK, "GET /health", "HTTP 200")
	get("/failures?q=missing", http.StatusOK, "No matching traces")
	traceID := telemetry.TraceID{1}
	var root telemetry.SpanID
	binary.BigEndian.PutUint16(root[:2], 1)
	var spans []telemetry.Span
	for i := 1; i <= 505; i++ {
		at := start.Add(time.Duration(i) * time.Millisecond)
		var id telemetry.SpanID
		binary.BigEndian.PutUint16(id[:2], uint16(i))
		span := telemetry.Span{
			TraceID: traceID, ID: id, ParentID: root,
			ServiceName: "checkout", Name: "checkout span", StartTime: at, EndTime: at.Add(time.Millisecond),
		}
		if i == 1 {
			span.ParentID = telemetry.SpanID{}
			span.Kind = telemetry.KindServer
			span.Attributes = telemetry.Attributes{"http.response.status_code": 200}
		}
		if i >= 3 && i <= 12 {
			span.StatusCode = telemetry.StatusError
			span.Name = "status evidence"
		}
		spans = append(spans, span)
	}
	early := &spans[1]
	early.ServiceName, early.Name = "inventory", "reserve stock"
	early.Events = []telemetry.Event{{
		Name: "exception", Time: early.StartTime,
		Attributes: telemetry.Attributes{"exception.message": "stock reservation failed"},
	}}
	evidence := &spans[len(spans)-1]
	evidence.ServiceName = "payments"
	evidence.Name = strings.Repeat("payment", 50)
	evidence.EndTime = evidence.StartTime.Add(-time.Millisecond)
	evidence.Events = []telemetry.Event{{
		Name: "exception", Time: evidence.StartTime,
		Attributes: telemetry.Attributes{"exception.message": "<script>alert(1)</script>", "exception.stacktrace": "/app/payments.go:12"},
	}}
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	tracePath := "/traces/" + hex.EncodeToString(traceID[:])
	evidenceID := hex.EncodeToString(evidence.ID[:])
	path := tracePath + "?span=" + evidenceID
	body := get("/failures?service=checkout&status=200&range=1h&q=alert", http.StatusOK,
		"HTTP 200", "Exception", path+"#span-details", "&lt;script&gt;")
	if strings.Contains(body, "<script>alert") {
		t.Fatal("telemetry was rendered as executable HTML")
	}
	get("/failures?evidence_service=payments", http.StatusOK, path+"#span-details")
	get("/failures?evidence_service=inventory&q=alert", http.StatusOK, "No matching traces")
	get("/failures?service=payments", http.StatusOK, "No matching traces")
	get("/failures?status=500", http.StatusOK, "No matching traces")
	get("/overview", http.StatusOK, "Most frequent operations", "checkout span", "Recent failures")
	anchor := hex.EncodeToString(spans[9].ID[:])
	get(tracePath, http.StatusOK, "Next spans", "checkout span", "reserve stock", path+"#span-details", "evidence_after="+anchor, "Partial map")
	get(tracePath+"?evidence_after="+anchor, http.StatusOK, "First evidence", "status evidence")
	get(path, http.StatusOK, "Span waterfall", "Span details", "aria-current=\"true\"", "payments.go:12", "Unavailable", "First spans", "Open source in VS Code", "vscode://file/app/payments.go:12")
	get(tracePath+"?span=0909090909090909", http.StatusOK, "Span unavailable")
	get("/traces/"+strings.Repeat("a", 32), http.StatusNotFound, "Trace not found")
}

func TestTraceEndpointForWildcardListeners(t *testing.T) {
	for _, tc := range []struct{ listen, host, want string }{
		{"127.0.0.1:4318", "127.0.0.1:4318", "http://127.0.0.1:4318/v1/traces"},
		{"0.0.0.0:4318", "geko.internal:8080", "http://geko.internal:4318/v1/traces"},
		{"[::]:4318", "[2001:db8::1]:8080", "http://[2001:db8::1]:4318/v1/traces"},
		{"[::]:4318", "", "http://127.0.0.1:4318/v1/traces"},
	} {
		if got := traceEndpoint(tc.listen, tc.host); got != tc.want {
			t.Errorf("traceEndpoint(%q, %q) = %q, want %q", tc.listen, tc.host, got, tc.want)
		}
	}
}

func TestSettingsForRemoteDeployment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	store, err := sqlite.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{
		ListenAddr: "0.0.0.0:4318", DBPath: dbPath, PublicURL: "https://geko.example.com",
		AccessToken: true, IngestTokens: []string{"storefront", "reporting"},
		ConfigSource: config.Source{PublicURL: config.SettingSource{Kind: "env", Variable: "GEKO_PUBLIC_URL"}},
	}); err != nil {
		t.Fatal(err)
	}
	for path, fragments := range map[string][]string{
		"/settings": {"https://geko.example.com/v1/traces", "Set by <code>GEKO_PUBLIC_URL</code>", "Access token required", "<code>storefront</code>, <code>reporting</code>"},
		"/failures": {"https://geko.example.com/v1/traces"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4318"+path, nil))
		for _, fragment := range fragments {
			if !strings.Contains(w.Body.String(), fragment) {
				t.Errorf("%s missing %q", path, fragment)
			}
		}
	}
}

func TestNotificationSettings(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fail := false
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Notifications: &Notifications{Format: "slack", Host: "hooks.slack.com", Test: func(context.Context) error {
		if fail {
			return errors.New("webhook returned 500")
		}
		return nil
	}}}); err != nil {
		t.Fatal(err)
	}
	for _, failing := range []bool{false, true} {
		fail = failing
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/settings/notifications/test", nil))
		want := "/settings?notification=sent#notifications"
		if failing {
			want = "/settings?notification=failed#notifications"
		}
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
			t.Fatalf("test notification = %d, %q", w.Code, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/settings?notification=failed", nil))
	for _, fragment := range []string{"<code>slack</code> to <code>hooks.slack.com</code>", "Send test notification", "test notification failed"} {
		if !strings.Contains(w.Body.String(), fragment) {
			t.Errorf("settings missing %q", fragment)
		}
	}
}

func TestMetrics(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Version: "v1.2.3", MaxDBBytes: 256 << 20, ReceiverDiagnostics: func() otlp.Diagnostics {
		return otlp.Diagnostics{AcceptedExports: 7, LastAccepted: time.Unix(1_700_000_000, 0), Rejections: []otlp.Rejection{{Reason: "Payload or batch limit exceeded", Count: 2}}}
	}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/metrics", nil))
	for _, fragment := range []string{
		`geko_build_info{version="v1.2.3"} 1`, "geko_receiver_accepted_exports_total 7",
		`geko_receiver_rejected_requests_total{reason="payload_or_batch_limit_exceeded"} 2`,
		"geko_receiver_last_accepted_timestamp_seconds 1700000000.000", `geko_storage_bytes{file="main"} `,
		"geko_storage_limit_bytes 268435456", "# TYPE geko_storage_evicted_traces_total counter",
	} {
		if !strings.Contains(w.Body.String(), fragment) {
			t.Errorf("metrics missing %q:\n%s", fragment, w.Body)
		}
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://evil.example/metrics", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("metrics on a non-loopback Host = %d", w.Code)
	}
}
