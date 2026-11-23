package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// TestRunHelperProcess is the command that geko run starts in TestRunCommand.
func TestRunHelperProcess(t *testing.T) {
	switch os.Getenv("GEKO_RUN_HELPER") {
	case "quiet":
		os.Exit(0)
	case "exit7":
		os.Exit(7)
	case "1":
	default:
		t.Skip("helper process")
	}
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},
"scopeSpans":[{"spans":[{"traceId":"0b000000000000000000000000000001","spanId":"0b00000000000001","name":"POST /pay","kind":2,
"startTimeUnixNano":"1000","endTimeUnixNano":"2000","status":{"code":2},
"attributes":[{"key":"http.response.status_code","value":{"intValue":"503"}}]}]}]}]}`
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") + "/v1/traces"
	noise := strings.NewReplacer("0b000000000000000000000000000001", "0b000000000000000000000000000002", "POST /pay", "unrelated failure").Replace(body)
	response, err := http.Post(endpoint, "application/json", strings.NewReader(noise))
	if err != nil || response.StatusCode != http.StatusOK {
		os.Exit(9)
	}
	response.Body.Close()
	_, runID, _ := strings.Cut(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), "geko.run.id=")
	body = strings.Replace(body, `"attributes":[{"key":"service.name"`, `"attributes":[{"key":"geko.run.id","value":{"stringValue":"`+runID+`"}},{"key":"service.name"`, 1)
	response, err = http.Post(endpoint, "application/json", strings.NewReader(body))
	if err != nil || response.StatusCode != http.StatusOK {
		os.Exit(9)
	}
	os.Exit(3)
}

func TestRunKeepsChildStatusWhenReportUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("GEKO_TOKEN", "")
	for _, helper := range []string{"quiet", "exit7"} {
		t.Run(helper, func(t *testing.T) {
			t.Setenv("GEKO_RUN_HELPER", helper)
			dir := t.TempDir()
			var stderr bytes.Buffer
			err := runCommand(t.Context(), []string{"--server", server.URL, "--baseline", filepath.Join(dir, "missing-baseline.json"), "--report", dir, "--", os.Args[0], "-test.run=^TestRunHelperProcess$"}, io.Discard, &stderr)
			var code exitError
			if helper == "quiet" && err != nil || helper == "exit7" && (!errors.As(err, &code) || code != 7) {
				t.Fatalf("child status = %v, stderr %s", err, &stderr)
			}
			if !strings.Contains(stderr.String(), "could not write the run report") {
				t.Fatalf("missing report diagnostic: %s", &stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, "report.json"))
			if err != nil || !bytes.Contains(data, []byte(`"telemetry_status": "unavailable"`)) {
				t.Fatalf("status = %s, %v", data, err)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_ACCESS_TOKEN", "")
	t.Setenv("GEKO_LISTEN_ADDR", addr)
	t.Setenv("GEKO_DB_PATH", filepath.Join(dir, "run.db"))
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	t.Setenv("GEKO_RUN_HELPER", "1")

	var stdout, stderr bytes.Buffer
	report := filepath.Join(dir, "report")
	err = runCommand(t.Context(), []string{"--report", report, "--", os.Args[0], "-test.run=^TestRunHelperProcess$"}, &stdout, &stderr)
	var code exitError
	if !errors.As(err, &code) || code != 3 {
		t.Fatalf("run error = %v, stderr %s", err, stderr.String())
	}
	summary := stderr.String()
	if strings.Contains(summary, "unrelated failure") {
		t.Fatalf("summary included unrelated telemetry: %s", summary)
	}
	for _, fragment := range []string{"failure evidence for run", "POST /pay  checkout · http_5xx", "/traces/0b000000000000000000000000000001", "list_failures with run_id=", "start geko serve to open the links"} {
		if !strings.Contains(summary, fragment) {
			t.Errorf("summary missing %q:\n%s", fragment, summary)
		}
	}

	markdown, err := os.ReadFile(filepath.Join(report, "report.md"))
	if err != nil || !strings.Contains(string(markdown), "1 failing trace in this run") || strings.Contains(string(markdown), "unrelated failure") {
		t.Fatalf("report.md = %s, %v", markdown, err)
	}
	if count, err := os.ReadFile(filepath.Join(report, "failures")); err != nil || string(count) != "1\n" {
		t.Fatalf("failures = %q, %v", count, err)
	}
	trace, err := os.ReadFile(filepath.Join(report, "traces", "0b000000000000000000000000000001.json"))
	if err != nil || !json.Valid(trace) || !bytes.Contains(trace, []byte("POST /pay")) {
		t.Fatalf("saved trace = %s, %v", trace, err)
	}

	if err := runCommand(t.Context(), nil, &stdout, &stderr); err == nil {
		t.Fatal("run without a command succeeded")
	}
	if err := runCommand(t.Context(), []string{filepath.Join(dir, "missing-command")}, &stdout, &stderr); err == nil || errors.As(err, &code) {
		t.Fatalf("missing command = %v", err)
	}
}

func TestRunReportsMissingExports(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_ACCESS_TOKEN", "")
	t.Setenv("GEKO_LISTEN_ADDR", addr)
	t.Setenv("GEKO_DB_PATH", filepath.Join(dir, "run.db"))
	t.Setenv("GEKO_RUN_HELPER", "quiet")
	var stderr bytes.Buffer
	if err := runCommand(t.Context(), []string{os.Args[0], "-test.run=^TestRunHelperProcess$"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "received no exports") {
		t.Fatalf("summary = %s", stderr.String())
	}
}

func TestRunQueriesRemoteByRunID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/failures" || r.Header.Get("Authorization") != "Bearer query-token" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		id := r.URL.Query().Get("run_id")
		if len(id) != 32 || r.URL.Query().Has("from") {
			http.Error(w, "expected run ID", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"run_id": id, "items": []any{}})
	}))
	defer server.Close()
	t.Setenv("GEKO_TOKEN", "query-token")
	t.Setenv("GEKO_RUN_HELPER", "quiet")
	t.Setenv("GEKO_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	var stderr bytes.Buffer
	if err := runCommand(t.Context(), []string{"--server", server.URL, "--", os.Args[0], "-test.run=^TestRunHelperProcess$"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "no failure evidence for run ") {
		t.Fatalf("output = %s", &stderr)
	}
}

func TestRunWarnsWhenGekoRequiresAToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Chdir(t.TempDir())
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_ACCESS_TOKEN", "")
	t.Setenv("GEKO_LISTEN_ADDR", strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("GEKO_RUN_HELPER", "quiet")
	var stderr bytes.Buffer
	if err := runCommand(t.Context(), []string{os.Args[0], "-test.run=^TestRunHelperProcess$"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if !strings.Contains(output, "exports will be rejected") || !strings.Contains(output, "could not summarize the run") ||
		strings.Contains(output, "no failure evidence") {
		t.Fatalf("output = %s", output)
	}
}

func TestReadReceiverCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "# TYPE geko_receiver_accepted_exports_total counter\ngeko_receiver_accepted_exports_total 7\n"+
			"geko_receiver_rejected_requests_total{reason=\"invalid_payload\"} 2\ngeko_receiver_rejected_requests_total{reason=\"capacity\"} 1\n")
	}))
	defer server.Close()
	counts, err := readReceiverCounts(t.Context(), server.URL, "secret")
	if err != nil || counts != (receiverCounts{accepted: 7, rejected: 3}) {
		t.Fatalf("counts = %+v, %v", counts, err)
	}
	if _, err := readReceiverCounts(t.Context(), server.URL, ""); !errors.Is(err, errTokenRequired) {
		t.Fatalf("missing token = %v", err)
	}
}

func TestRunEnvironmentKeepsCallerSettings(t *testing.T) {
	t.Setenv("GEKO_ACCESS_TOKEN", "")
	env := runEnvironment([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318", "OTEL_RESOURCE_ATTRIBUTES=service.namespace=store,geko.run.id=old", "PATH=/bin"}, "http://127.0.0.1:4318", "fresh", "secret")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318") || !strings.Contains(joined, "OTEL_LOGS_EXPORTER=none") ||
		!strings.Contains(joined, "service.namespace=store,geko.run.id=fresh") || strings.Contains(joined, "geko.run.id=old") || strings.Contains(joined, "secret") {
		t.Fatalf("environment = %s", joined)
	}
	if env := strings.Join(runEnvironment(nil, "http://127.0.0.1:4318", "fresh", "3f9c"), "\n"); !strings.Contains(env, "OTEL_EXPORTER_OTLP_TRACES_HEADERS=authorization=Bearer%203f9c") {
		t.Fatalf("environment without token header = %s", env)
	}
}

func TestRunReportKeepsTelemetryInert(t *testing.T) {
	status := 502
	items := []investigation.TraceSummary{{
		TraceID:   "0b000000000000000000000000000001",
		EntrySpan: &investigation.SpanSummary{Operation: "POST /pay", Service: "checkout", HTTPStatus: &status},
		Evidence: &investigation.Evidence{SpanSummary: investigation.SpanSummary{Operation: "charge", Service: "payments"},
			Classification: "exception", ExceptionType: "Timeout",
			ExceptionMessage: "@octocat see ![x](https://evil.example/x.png) | `rm -rf` \n" + strings.Repeat("a", 200)},
	}}
	report := runReport("abc", items, true)
	for _, want := range []string{"More than 1 failing trace", "`@octocat see ![x](https://evil.example/x.png) \\| 'rm -rf'", "…`", "`POST /pay` in `checkout` · HTTP 502"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Count(report, "\n") != 9 {
		t.Errorf("a telemetry newline broke the table:\n%s", report)
	}
	if empty := runReport("abc", nil, false); !strings.Contains(empty, "no failure evidence") {
		t.Errorf("empty report = %s", empty)
	}
}
