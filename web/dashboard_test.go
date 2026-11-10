package web

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"geko/internal/storage/sqlite"
	"geko/internal/telemetry"
)

func TestDashboardInvestigation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dashboard.db")
	store, err := sqlite.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{ListenAddr: "127.0.0.1:4318", DBPath: dbPath, Version: "test"}); err != nil {
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
	get("/failures", http.StatusOK, "No failures observed")
	get("/assets/dashboard.css", http.StatusOK, ".waterfall")
	get("/assets/dashboard.js", http.StatusOK, "clipboard.writeText")
	get("/settings", http.StatusOK, "schema 2", "SQLite sidecars", "127.0.0.1:4318")

	start := time.Now().UTC().Add(-time.Minute)
	traceID := telemetry.TraceID{1}
	root := telemetry.SpanID{1}
	var spans []telemetry.Span
	for i := 1; i <= 55; i++ {
		at := start.Add(time.Duration(i) * time.Millisecond)
		span := telemetry.Span{
			TraceID: traceID, ID: telemetry.SpanID{byte(i)}, ParentID: root,
			ServiceName: "checkout", Name: "checkout span", StartTime: at, EndTime: at.Add(time.Millisecond),
		}
		if i == 1 {
			span.ParentID = telemetry.SpanID{}
			span.Kind = telemetry.KindServer
			span.Attributes = telemetry.Attributes{"http.response.status_code": 200}
		}
		spans = append(spans, span)
	}
	evidence := &spans[len(spans)-1]
	evidence.ServiceName = "payments"
	evidence.Name = strings.Repeat("payment", 50)
	evidence.EndTime = evidence.StartTime.Add(-time.Millisecond)
	evidence.Events = []telemetry.Event{{
		Name: "exception", Time: evidence.StartTime,
		Attributes: telemetry.Attributes{"exception.message": "<script>alert(1)</script>", "exception.stacktrace": "payments.go:12"},
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
	get("/failures?status=500", http.StatusOK, "No matching traces")
	get("/overview", http.StatusOK, "Most frequent operations", "checkout span", "Recent failures")
	get(tracePath, http.StatusOK, "Next spans", "checkout span")
	get(path, http.StatusOK, "Span waterfall", "Span details", "aria-current=\"true\"", "payments.go:12", "Unavailable", "First spans")
	get(tracePath+"?span=0909090909090909", http.StatusOK, "Span unavailable")
	get("/traces/"+strings.Repeat("a", 32), http.StatusNotFound, "Trace not found")
}
