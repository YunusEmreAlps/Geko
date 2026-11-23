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

func TestRunComparisonPageShowsObservedEvidenceWithoutResolving(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	baseline, candidate := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for i, id := range []string{baseline, candidate} {
		span := telemetry.Span{TraceID: telemetry.TraceID{byte(i + 1)}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /work", Kind: telemetry.KindServer,
			StartTime: time.Now(), EndTime: time.Now(), ResourceAttributes: telemetry.Attributes{"service.namespace": "Shop", "geko.run.id": id}}
		if i == 0 {
			span.Events = []telemetry.Event{{Name: "exception", Attributes: telemetry.Attributes{"exception.type": "<script>unsafe</script>"}}}
		}
		if err := store.WriteSpans(t.Context(), time.Now(), []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query  string
		status int
		want   string
	}{
		{"", 200, "Baseline run ID"},
		{"?baseline=" + baseline + "&candidate=" + candidate, 200, "Not observed in candidate"},
		{"?baseline=" + baseline + "&candidate=" + strings.Repeat("c", 32), 200, "Comparison unavailable"},
		{"?baseline=invalid&candidate=" + candidate, 400, "Choose two different"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/runs/compare"+test.query, nil))
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.want) {
			t.Fatalf("%s: %d %s", test.query, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "<script>unsafe</script>") {
			t.Fatal("telemetry was rendered as HTML")
		}
	}
}
