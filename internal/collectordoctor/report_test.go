package collectordoctor

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConfigFindings(t *testing.T) {
	result, err := configFindings([]byte(`
receivers:
  otlp: {}
processors:
  batch: {}
  memory_limiter: {}
exporters:
  otlp/backend:
    headers:
      api_key: secret-never-report
    sending_queue: {enabled: false}
connectors:
  spanmetrics: {}
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch, memory_limiter, unknown]
      exporters: [otlp/backend, spanmetrics]
    metrics:
      receivers: [spanmetrics]
      exporters: [otlp/backend]
`))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, finding := range result {
		codes = append(codes, finding.Code)
		if strings.Contains(finding.Evidence, "secret") {
			t.Fatal("configuration secret leaked")
		}
	}
	if want := []string{"memory_limiter_order", "missing_component", "queue_disabled"}; !slices.Equal(codes, want) {
		t.Fatalf("findings = %+v", result)
	}
}

func TestCollectorMetricDeltaAndQueueLabels(t *testing.T) {
	before := []byte(`otelcol_exporter_send_failed_spans{exporter="otlp/a",service_instance_id="1"} 7
otelcol_exporter_enqueue_failed_metric_points{exporter="otlp/a",service_instance_id="1"} 2
otelcol_receiver_refused_log_records{receiver="otlp",service_instance_id="old"} 99
`)
	after := []byte(`otelcol_exporter_send_failed_spans_total{service_instance_id="1",exporter="otlp/a"} 7
otelcol_exporter_enqueue_failed_metric_points_total{exporter="otlp/a",service_instance_id="1"} 5
otelcol_exporter_queue_size{exporter="otlp/a",service_instance_id="1"} 90
otelcol_exporter_queue_capacity{service_instance_id="1",exporter="otlp/a"} 100
otelcol_exporter_queue_capacity{service_instance_id="2",exporter="otlp/a"} 1000
otelcol_receiver_refused_log_records{receiver="otlp",service_instance_id="new"} 4
`)
	findings, err := metricFindings(after, before)
	if err != nil || len(findings) != 3 || findings[0].Code != "enqueue_failed" ||
		!strings.Contains(findings[0].Evidence, "= 3") || findings[1].Code != "queue_pressure" ||
		findings[2].Code != "unmatched_counters" {
		t.Fatalf("findings = %+v, %v", findings, err)
	}
}

func TestInspectionSamplesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/metrics", http.StatusFound)
			return
		}
		w.Write([]byte("otelcol_exporter_send_failed_spans{exporter=\"otlp\"} 4\n"))
	}))
	defer server.Close()
	report := Inspect(t.Context(), Options{Sources: Sources{MetricsURL: server.URL + "/metrics"}, SampleInterval: time.Millisecond})
	if !report.Sampled || len(report.Findings) != 0 {
		t.Fatalf("report = %+v", report)
	}
	report = Inspect(t.Context(), Options{Sources: Sources{MetricsURL: server.URL + "/redirect"}})
	if !report.HasErrors() || report.MetricsChecked {
		t.Fatalf("redirect = %+v", report)
	}
}

func TestUnresolvedConfigIsReportedWithoutLeakingInput(t *testing.T) {
	findings, err := configFindings([]byte("receivers:\n  otlp: ${file:secret-path}\nexporters:\n  debug: {}\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Code == "config_unresolved" {
			found = true
		}
		if strings.Contains(f.Evidence, "secret-path") {
			t.Fatal("provider path leaked")
		}
	}
	if !found {
		t.Fatalf("missing unresolved input diagnostic: %+v", findings)
	}
}

func TestUnresolvedWholePipelineRemainsPartial(t *testing.T) {
	findings, err := configFindings([]byte("service:\n  pipelines: ${file:private-file}\n"))
	if err != nil || len(findings) != 1 || findings[0].Code != "config_unresolved" {
		t.Fatalf("unresolved pipeline should explain partial coverage: %+v, %v", findings, err)
	}
}

func TestUnresolvedDocumentCannotHideAdditionalDocuments(t *testing.T) {
	if findings, err := configFindings([]byte("service:\n  pipelines: ${file:private}\n---\nexporters:\n  debug: {}\n")); err == nil {
		t.Fatalf("multiple documents accepted with partial substitution: %+v", findings)
	}
}

func TestEscapedDollarDoesNotRequireProviderResolution(t *testing.T) {
	findings, err := configFindings([]byte("receivers:\n  otlp: {}\nexporters:\n  debug:\n    endpoint: http://localhost/$$TOKEN\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "config_unresolved" {
			t.Fatalf("literal escaped dollar treated as substitution: %+v", f)
		}
	}
}
