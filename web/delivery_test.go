package web

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

func TestDeliveryScopesAndUnknownEvidence(t *testing.T) {
	now := time.Now()
	report := buildDelivery(connectView{Service: "api"}, Settings{}, nil, nil, nil, now)
	if len(report.Stages) != 4 {
		t.Fatalf("stages: %+v", report)
	}
	for _, stage := range report.Stages {
		if stage.State == "Healthy" {
			t.Fatal("unknown evidence claimed healthy")
		}
	}
	if report.Stages[0].State != "Unavailable" || report.Stages[1].State != "Unavailable" || report.Stages[2].State != "Unavailable" || report.Stages[3].State != "Not configured" {
		t.Fatalf("unknown: %+v", report)
	}
	view := connectView{Service: "api", Check: connectionCheck{Available: true, Started: true, Expired: true}}
	report = buildDelivery(view, Settings{}, &otlp.Diagnostics{Rejections: []otlp.Rejection{{Reason: "Receiver busy", Count: 9}}}, &storage.Status{}, nil, now)
	if report.Stages[0].State != "No matching sample" || report.Stages[1].State != "Past rejections" || !strings.Contains(strings.ToLower(report.Stages[1].Scope), "all applications") {
		t.Fatalf("scope: %+v", report)
	}
	view.Check.Samples = []otlp.ServiceObservation{{Service: "api"}}
	report = buildDelivery(view, Settings{ReadOnly: true}, nil, &storage.Status{}, nil, now)
	if report.Stages[0].State != "Observed" || report.Stages[1].State != "Read-only" {
		t.Fatalf("read-only: %+v", report)
	}
}
func TestDeliveryStorageLimitDoesNotClaimCurrentLoss(t *testing.T) {
	report := buildDelivery(connectView{}, Settings{MaxDBBytes: 100, OnFull: storage.Reject}, nil, &storage.Status{MainBytes: 120, FreeBytes: 40, EvictedTraces: 3, RejectedExports: 2}, nil, time.Now())
	stage := report.Stages[2]
	if stage.State != "Metadata available" || !strings.Contains(stage.Evidence, "40") || strings.Contains(stage.State, "full") || !strings.Contains(stage.Scope, "database lifetime") {
		t.Fatalf("storage: %+v", stage)
	}
}

func TestDeliveryCollectorSnapshotAndSafeReturn(t *testing.T) {
	now := time.Now()
	summary := summarizeCollector(collectordoctor.Report{ConfigChecked: true, Findings: []collectordoctor.Finding{{Severity: "error", Evidence: "private-marker"}, {Severity: "warning"}}}, now)
	report := buildDelivery(connectView{}, Settings{CollectorDoctor: collectordoctor.Sources{ConfigFile: "private-path"}}, nil, nil, &summary, now.Add(6*time.Minute))
	if summary.Errors != 1 || summary.Warnings != 1 || report.Stages[3].State != "Previous check is stale" {
		t.Fatalf("collector summary: %+v", report)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "private-marker") || strings.Contains(string(encoded), "private-path") {
		t.Fatal("shared diagnostic leaked source data")
	}
	settings := Settings{IngestTokens: []string{"app-token"}}
	for _, raw := range []string{"https://evil.test/connect", "//evil.test/connect", "/settings", "/connect?service=bad%3Bcommand", "/connect?token=actual-secret"} {
		if safeConnectReturn(raw, settings) != "/connect" {
			t.Fatalf("unsafe return %q", raw)
		}
	}
	got := safeConnectReturn("/connect?service=api&application=shop&protocol=http%2Fprotobuf&token=app-token&since=1800000000000&q=private", settings)
	u, _ := url.Parse(got)
	if u.Path != "/connect" || u.Query().Get("service") != "api" || u.Query().Get("since") != "1800000000000" || u.Query().Has("q") {
		t.Fatalf("return: %s", got)
	}
}

type unavailableDeliveryStorage struct{ Reader }

func (unavailableDeliveryStorage) Status(context.Context) (storage.Status, error) {
	return storage.Status{}, errors.New("private-storage-error")
}

func TestDeliveryPagePartialFailureAndNoImplicitCollector(t *testing.T) {
	var scrapes atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scrapes.Add(1)
		w.Write([]byte("otelcol_exporter_send_failed_spans_total{exporter=\"otlp/private-marker\"} 3\n"))
	}))
	defer collector.Close()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, unavailableDeliveryStorage{store}, Settings{CollectorDoctor: collectordoctor.Sources{MetricsURL: collector.URL}}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		return w
	}
	first := get("/connect?service=api")
	if first.Code != 200 || !strings.Contains(first.Body.String(), "Delivery diagnostics") || !strings.Contains(first.Body.String(), "Storage metadata could not be read") || !strings.Contains(first.Body.String(), "OTEL_SERVICE_NAME=api") || strings.Contains(first.Body.String(), "private-storage-error") {
		t.Fatalf("partial diagnostics: %d %s", first.Code, first.Body)
	}
	if scrapes.Load() != 0 {
		t.Fatal("Connect scraped Collector implicitly")
	}
	doctor := get("/collector-doctor?return=" + url.QueryEscape("/connect?service=api"))
	if doctor.Code != 200 || scrapes.Load() != 1 || !strings.Contains(doctor.Body.String(), "Back to connection check") {
		t.Fatalf("manual check: %d", doctor.Code)
	}
	next := get("/connect?service=api")
	if scrapes.Load() != 1 || !strings.Contains(next.Body.String(), "Last check available") || strings.Contains(next.Body.String(), "private-marker") || strings.Contains(next.Body.String(), collector.URL) {
		t.Fatal("cached Collector summary missing, leaked data or re-scraped")
	}
}

func TestDeliveryStatusCancellationAndAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := dashboard{store: unavailableDeliveryStorage{}, slots: make(chan struct{}, 1)}
	request := httptest.NewRequestWithContext(ctx, "GET", "http://127.0.0.1/connect", nil)
	if d.deliveryStatus(request) != nil || len(d.slots) != 0 {
		t.Fatal("cancelled diagnostic took a slot")
	}
	d.slots <- struct{}{}
	if d.deliveryStatus(httptest.NewRequest("GET", "http://127.0.0.1/connect", nil)) != nil || len(d.slots) != 1 {
		t.Fatal("busy diagnostics disturbed admission")
	}
}

func TestDeliveryConcurrentCollectorChecksAndConnectReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.yaml")
	if err := os.WriteFile(path, []byte("service: {pipelines: {}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := Register(mux, unavailableDeliveryStorage{}, Settings{CollectorDoctor: collectordoctor.Sources{ConfigFile: path}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	get := func(path string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1"+path, nil))
		if w.Code != 200 {
			t.Errorf("concurrent diagnostic: %d", w.Code)
		}
	}
	wg.Go(func() {
		for range 20 {
			get("/collector-doctor")
		}
	})
	wg.Go(func() {
		for range 30 {
			get("/connect?service=api")
		}
	})
	wg.Wait()
}

type cancellingDeliveryStorage struct {
	Reader
	cancel context.CancelFunc
}

func (s cancellingDeliveryStorage) Status(context.Context) (storage.Status, error) {
	s.cancel()
	return storage.Status{MainBytes: 123}, nil
}
func TestDeliveryDoesNotPublishCancelledMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := dashboard{store: cancellingDeliveryStorage{cancel: cancel}, slots: make(chan struct{}, 1)}
	request := httptest.NewRequestWithContext(ctx, "GET", "http://127.0.0.1/connect", nil)
	if d.deliveryStatus(request) != nil {
		t.Fatal("metadata published after cancellation")
	}
	if len(d.slots) != 0 {
		t.Fatal("query slot leaked")
	}
}
