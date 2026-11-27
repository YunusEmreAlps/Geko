package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestRuntimeBudgetAdminFormAPIAndExistingTracker(t *testing.T) {
	data := []byte("telemetry_budget:\n  enabled: true\n")
	file := filepath.Join(t.TempDir(), "geko.yaml")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{TelemetryBudget: budget.Config{Enabled: true}, Source: config.Source{FilePath: file, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data))}}
	runtime := config.NewRuntime(cfg)
	now := time.Now()
	tracker := budget.New(cfg.TelemetryBudget, now).WithPolicySource(runtime.BudgetPolicy)
	tracker.Record(now, []telemetry.Span{{ServiceName: "api"}, {ServiceName: "api"}}, []budget.Observation{{Span: &trace.Span{}}, {Span: &trace.Span{}}})
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var auditLog bytes.Buffer
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Runtime: runtime, TelemetryBudget: tracker, Audit: audit.New(&auditLog)}); err != nil {
		t.Fatal(err)
	}
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1"+target, strings.NewReader(body))
		if strings.HasPrefix(target, "/api/") {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := do("GET", "/settings", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `action="/settings/budget"`) {
		t.Fatalf("editor missing: %d", w.Code)
	}
	before := runtime.Snapshot().Revision
	body := `{"revision":"` + before + `","limits":{"default":{"spans_per_hour":1},"services":[{"service":"private-budget-marker","spans_per_hour":17}],"cardinality_limit":2}}`
	if w := do("POST", "/api/v1/settings/budget", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := tracker.Snapshot(now, false); got.Total.Spans != 2 || got.Services[0].SpanOverage != 1 {
		t.Fatalf("existing tracker: %+v", got)
	}
	if w := do("POST", "/api/v1/settings/budget", body); w.Code != 409 {
		t.Fatalf("stale save: %d", w.Code)
	}
	for _, invalid := range []string{`{"limits":{"enabled":true}}`, `{"limits":{"default":{"spans_per_hour":-1}}}`, `{"limits":null}`} {
		invalid = strings.Replace(invalid, `{`, `{"revision":"`+runtime.Snapshot().Revision+`",`, 1)
		if w := do("POST", "/api/v1/settings/budget", invalid); w.Code != 400 {
			t.Fatalf("invalid: %d %s", w.Code, w.Body)
		}
	}
	form := url.Values{"revision": {runtime.Snapshot().Revision}, "budget_yaml": {"default: {spans_per_hour: 5}\ncardinality_limit: 3"}}
	if w := do("POST", "/settings/budget", form.Encode()); w.Code != 303 || w.Header().Get("Location") != "/settings#budget-limits" {
		t.Fatalf("form: %d %s", w.Code, w.Body)
	}
	if strings.Contains(auditLog.String(), "private-budget-marker") || !strings.Contains(auditLog.String(), "budget_limits") {
		t.Fatal("audit leaked limits or missed update")
	}
	if got := tracker.Snapshot(now, false); got.Total.Spans != 2 || got.Services[0].SpanOverage != 0 {
		t.Fatalf("form reset: %+v", got)
	}
}
