package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestApplicationGrouping(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "applications.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC)
	record := func(trace, id, parent byte, service, operation, namespace string, observed time.Time) {
		t.Helper()
		span := telemetry.Span{
			TraceID: telemetry.TraceID{trace}, ID: telemetry.SpanID{id}, ParentID: telemetry.SpanID{parent},
			ServiceName: service, Name: operation, Kind: telemetry.KindServer, StatusCode: telemetry.StatusError,
			StartTime: observed, EndTime: observed.Add(time.Second),
		}
		if namespace != "" {
			span.ResourceAttributes = telemetry.Attributes{"service.namespace": namespace}
		}
		if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	record(1, 1, 0, "api", "cross-project entry", "Kavun", start)
	record(1, 2, 1, "api", "cross-project child", "Nar", start)
	record(2, 1, 0, "api", "old Kavun request", "", start.Add(time.Minute))
	record(3, 1, 0, "api", "old Nar request", "", start.Add(2*time.Minute))
	record(4, 1, 0, "worker", "old worker request", "", start.Add(3*time.Minute))

	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, w.Code)
		}
		return w.Body.String()
	}
	post := func(form url.Values, origin string, status int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/applications", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("application POST returned %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	contains := func(body, text string, want bool) {
		t.Helper()
		if strings.Contains(body, text) != want {
			t.Errorf("contains %q = %v, want %v", text, !want, want)
		}
	}

	for _, app := range []string{"Kavun", "Nar"} {
		body := get("/failures?application=group:" + app)
		contains(body, "cross-project entry", true)
		contains(body, "old Kavun request", false)
	}
	contains(get("/failures?application=group:Nar&evidence_service=api"), "?span=0200000000000000#span-details", true)
	contains(get("/failures?application=group:Nar&service=api"), "cross-project entry", false)
	contains(get("/traces/01000000000000000000000000000000"), "Kavun / api → Nar / api", true)
	contains(get("/failures?application=ungrouped"), "old Kavun request", true)

	mapping := url.Values{"action": {"mapping"}, "service": {"api"}, "application": {"Kavun"}}
	post(mapping, "http://127.0.0.1:8080", http.StatusForbidden)
	contains(get("/failures?application=group:Kavun"), "old Kavun request", false)
	post(mapping, "http://127.0.0.1", http.StatusSeeOther)
	contains(get("/failures?application=group:Kavun"), "old Kavun request", true)
	contains(get("/failures?application=group:Nar"), "cross-project entry", true)
	contains(get("/applications"), "<dt>api</dt><dd>Kavun</dd>", true)

	assignment := url.Values{
		"action": {"assign"}, "service": {"api"}, "application": {"Nar"},
		"from": {"2026-01-01T10:02"}, "to": {"2026-01-01T10:03"},
	}
	post(assignment, "http://127.0.0.1", http.StatusSeeOther)
	body := get("/failures?application=group:Nar")
	contains(body, "old Nar request", true)
	contains(body, "old Kavun request", false)
	contains(body, "old worker request", false)
	assignment.Set("action", "assign-clear")
	post(assignment, "http://127.0.0.1", http.StatusSeeOther)
	contains(get("/failures?application=group:Kavun"), "old Nar request", true)
}
