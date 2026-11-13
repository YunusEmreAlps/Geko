package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunHelperProcess is the command that geko run starts in TestRunCommand.
func TestRunHelperProcess(t *testing.T) {
	switch os.Getenv("GEKO_RUN_HELPER") {
	case "quiet":
		os.Exit(0)
	case "1":
	default:
		t.Skip("helper process")
	}
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},
"scopeSpans":[{"spans":[{"traceId":"0b000000000000000000000000000001","spanId":"0b00000000000001","name":"POST /pay","kind":2,
"startTimeUnixNano":"1000","endTimeUnixNano":"2000","status":{"code":2},
"attributes":[{"key":"http.response.status_code","value":{"intValue":"503"}}]}]}]}]}`
	response, err := http.Post(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")+"/v1/traces", "application/json", strings.NewReader(body))
	if err != nil || response.StatusCode != http.StatusOK {
		os.Exit(9)
	}
	os.Exit(3)
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
	err = runCommand(t.Context(), []string{"--", os.Args[0], "-test.run=^TestRunHelperProcess$"}, &stdout, &stderr)
	var code exitError
	if !errors.As(err, &code) || code != 3 {
		t.Fatalf("run error = %v, stderr %s", err, stderr.String())
	}
	summary := stderr.String()
	for _, fragment := range []string{"Matched by time, not by test", "POST /pay  checkout · http_5xx", "/traces/0b000000000000000000000000000001", "list_failures with from=", "start geko serve to open the links"} {
		if !strings.Contains(summary, fragment) {
			t.Errorf("summary missing %q:\n%s", fragment, summary)
		}
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
	env := runEnvironment([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318", "PATH=/bin"}, "http://127.0.0.1:4318")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318") || !strings.Contains(joined, "OTEL_LOGS_EXPORTER=none") {
		t.Fatalf("environment = %s", joined)
	}
	t.Setenv("GEKO_ACCESS_TOKEN", "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8")
	if env := strings.Join(runEnvironment(nil, "http://127.0.0.1:4318"), "\n"); !strings.Contains(env, "OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer%203f9c") {
		t.Fatalf("environment without token header = %s", env)
	}
}
