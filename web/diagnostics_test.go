package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestDiagnosticPages(t *testing.T) {
	directory := t.TempDir()
	store, err := sqlite.Open(t.Context(), filepath.Join(directory, "geko.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(directory, "collector.yaml")
	if err := os.WriteFile(path, []byte("service:\n  pipelines:\n    traces:\n      receivers: [missing]\n      exporters: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tracker := budget.New(budget.Config{Enabled: true, Defaults: budget.Limits{Bytes: 1}}, now)
	tracker.Record(now, []telemetry.Span{{ServiceName: "gateway"}},
		[]budget.Observation{{Span: &trace.Span{Name: "GET /test"}}})
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{
		CollectorDoctor: collectordoctor.Sources{ConfigFile: path}, TelemetryBudget: tracker,
	}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
		return response.Body.String()
	}
	if body := get("/collector-doctor"); !strings.Contains(body, "missing") || !strings.Contains(body, "Suggested action") ||
		strings.Contains(body, "Compare 5s samples") {
		t.Fatal("invalid config-only Collector report")
	}
	if body := get("/budget"); !strings.Contains(body, "gateway") || !strings.Contains(body, "Ways to reduce volume") {
		t.Fatal("missing budget overage")
	}
	var report budget.Report
	if err := json.Unmarshal([]byte(get("/api/v1/budget")), &report); err != nil ||
		report.Total.Spans != 1 || len(report.Services) != 1 || report.Services[0].ByteOverage <= 0 {
		t.Fatalf("budget API = %+v, %v", report, err)
	}
}

func TestCollectorDoctorDashboardComparesLiveSamples(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	var reads atomic.Int32
	firstScrape := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		read := reads.Add(1)
		fmt.Fprintf(w, "otelcol_exporter_enqueue_failed_spans_total{exporter=\"otlp/backend\"} %d\n", 4+3*read)
		if read == 1 {
			close(firstScrape)
		}
	}))
	defer collector.Close()
	mux := http.NewServeMux()
	if err := Register(mux, nil, Settings{CollectorDoctor: collectordoctor.Sources{MetricsURL: collector.URL}}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+path, nil))
		return result
	}
	const path = "/collector-doctor?sample=true"
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- get(path) }()
	select {
	case <-firstScrape:
	case <-ctx.Done():
		t.Fatal("first Collector scrape did not start")
	}
	if busy := get(path); busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") == "" {
		t.Fatalf("overlapping comparison = %d %s", busy.Code, busy.Body)
	}
	select {
	case report := <-finished:
		if report.Code != http.StatusOK || !strings.Contains(report.Body.String(), "= 3 (increase between samples)") ||
			!strings.Contains(report.Body.String(), "Current snapshot") {
			t.Fatalf("comparison = %d %s", report.Code, report.Body)
		}
	case <-ctx.Done():
		t.Fatal("Collector comparison did not complete")
	}
	if snapshot := get("/collector-doctor"); snapshot.Code != http.StatusOK ||
		!strings.Contains(snapshot.Body.String(), "= 13 (cumulative sample)") ||
		!strings.Contains(snapshot.Body.String(), "Compare 5s samples") {
		t.Fatalf("snapshot = %d %s", snapshot.Code, snapshot.Body)
	}
}
