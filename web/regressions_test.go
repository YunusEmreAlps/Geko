package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestVersionRegressions(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	var spans []telemetry.Span
	add := func(version, env, operation string, count int, failing bool, duration time.Duration) {
		for range count {
			root := telemetry.Span{
				TraceID: telemetry.TraceID{byte(len(spans) + 1)}, ID: telemetry.SpanID{1},
				Kind: telemetry.KindServer, ServiceName: "orders", Name: operation,
				StartTime: now, EndTime: now.Add(duration),
				Attributes:         telemetry.Attributes{"http.response.status_code": int64(200)},
				ResourceAttributes: telemetry.Attributes{"service.version": version, "deployment.environment.name": env},
			}
			spans = append(spans, root)
			if failing {
				child := root
				child.ID, child.ParentID, child.StatusCode = telemetry.SpanID{2}, root.ID, telemetry.StatusError
				spans = append(spans, child)
			}
		}
	}
	add("v1", "prod", "GET /orders", 10, false, 100*time.Millisecond)
	add("v2", "prod", "GET /orders", 10, true, 130*time.Millisecond)
	add("v2", "staging", "GET /orders", 10, false, time.Millisecond)
	add("v1", "prod", "GET /small", 1, false, time.Millisecond)
	add("v2", "prod", "GET /small", 1, true, 2*time.Millisecond)
	add("v1", "prod", "GET /invalid-timing", 10, false, -time.Millisecond)
	add("v2", "prod", "GET /invalid-timing", 10, false, -time.Millisecond)
	if err := store.WriteSpans(t.Context(), now, spans); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/regressions?baseline=v1&candidate=v2&service=orders", nil))
	body := w.Body.String()
	for _, expected := range []string{"Failure rate and latency", "100.0% with evidence (10 / 10)", "130ms mean", "Too few requests", "Too few valid durations"} {
		if w.Code != http.StatusOK || !strings.Contains(body, expected) {
			t.Fatalf("missing %q in response %d: %s", expected, w.Code, body)
		}
	}
	if strings.Contains(body, "staging") || strings.Count(body, ">Failure rate and latency<") != 1 {
		t.Fatal("unmatched environment or small cohort flagged")
	}
}
