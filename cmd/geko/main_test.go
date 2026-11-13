package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestVersion(t *testing.T) {
	t.Setenv("GEKO_LISTEN_ADDR", "invalid")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Geko "+version) || !strings.Contains(out.String(), "go1.") {
		t.Fatalf("unexpected version output %q", out.String())
	}
}

func TestDoctorReportsBusyListener(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_LISTEN_ADDR", "")
	t.Setenv("GEKO_DB_PATH", "")
	listener, err := listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	yaml := "listen_addr: '" + listener.Addr().String() + "'\ndb_path: doctor.db\n"
	if err := os.WriteFile("geko.yaml", []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = run(t.Context(), []string{"doctor"}, &out)
	if err == nil || !strings.Contains(err.Error(), "GEKO_LISTEN_ADDR") {
		t.Fatalf("doctor error = %v, want a listener configuration hint", err)
	}
	if !strings.Contains(out.String(), "schema 9") {
		t.Fatalf("doctor output %q does not include the database result", out.String())
	}
	if _, err := os.Stat("doctor.db"); err != nil {
		t.Fatal(err)
	}
}

func TestServeOTLPQueryAndShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cfg := config.Config{
		ListenAddr:    "127.0.0.1:0",
		DBPath:        filepath.Join(t.TempDir(), "geko.db"),
		RetentionDays: config.DefaultRetentionDays,
		MaxDBMiB:      config.DefaultMaxDBMiB,
	}
	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	expired := telemetry.Span{
		TraceID: telemetry.TraceID{0xaa}, ID: telemetry.SpanID{1},
		Name: "expired fixture", ServiceName: "fixture", StatusCode: telemetry.StatusError,
		StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0),
	}
	if err := errors.Join(db.WriteSpans(ctx, time.Now().Add(-8*24*time.Hour), []telemetry.Span{expired}), db.Close()); err != nil {
		t.Fatal(err)
	}
	ready := make(startupOutput, 1)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ready) }()

	var baseURL string
	select {
	case line := <-ready:
		baseURL = strings.TrimSpace(strings.TrimPrefix(line, "Geko listening on "))
	case err := <-done:
		t.Fatalf("serve exited before startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("health HTTP status = %d, want 204", resp.StatusCode)
	}

	resp, err = client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	failuresHTML, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || !strings.Contains(string(failuresHTML), "No traces received yet") {
		t.Fatalf("startup retention did not clear the expired fixture: %v", err)
	}
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/failures" ||
		resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("dashboard entry = %d %s %s, want the failures page", resp.StatusCode, resp.Request.URL.Path, resp.Header.Get("Content-Type"))
	}

	body, err := proto.Marshal(&collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{{
			TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8),
			Name: "GET /checkout", Kind: trace.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: 100, EndTimeUnixNano: 200,
			Attributes: []*common.KeyValue{{
				Key:   "http.response.status_code",
				Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 500}},
			}},
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Post(baseURL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("OTLP export = %d %s, want 200 protobuf", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	resp, err = client.Post(baseURL+"/v1/traces", "application/x-protobuf", bytes.NewReader([]byte{0xff}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed export = %d, want 400", resp.StatusCode)
	}

	resp, err = client.Get(baseURL + "/settings")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("settings = %d, %v", resp.StatusCode, err)
	}
	for _, want := range []string{
		"Accepted exports with spans</dt><dd>1", "Invalid payload</dt><dd>1 rejected",
		"7 days since last receipt", "Main database limit</dt><dd>256.0 MiB",
		"No YAML file loaded",
	} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("settings missing %q", want)
		}
	}
	if strings.Contains(string(settings), "None this session") {
		t.Error("last accepted export is missing after successful ingest")
	}

	resp, err = client.Get(baseURL + "/api/v1/failures?status=500")
	if err != nil {
		t.Fatal(err)
	}
	var failures struct {
		Items []investigation.TraceSummary `json:"items"`
	}
	err = json.NewDecoder(resp.Body).Decode(&failures)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || len(failures.Items) != 1 || failures.Items[0].HTTPOutcome != "server_error" {
		t.Fatalf("failure list = %d %+v, %v", resp.StatusCode, failures, err)
	}
	resp, err = client.Get(baseURL + "/api/v1/traces/" + failures.Items[0].TraceID)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Trace investigation.TraceSummary `json:"trace"`
		Spans []investigation.Span       `json:"spans"`
	}
	err = json.NewDecoder(resp.Body).Decode(&detail)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || len(detail.Spans) != 1 ||
		detail.Trace.Evidence == nil || detail.Trace.Evidence.SpanID != detail.Spans[0].SpanID {
		t.Fatalf("trace details = %d %+v, %v", resp.StatusCode, detail, err)
	}
	resp, err = client.Get(baseURL + "/api/v1/traces/" + strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing trace status = %d, want 404", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("server did not shut down")
	}

	listener, err := net.Listen("tcp", strings.TrimPrefix(baseURL, "http://"))
	if err != nil {
		t.Fatalf("listener not released: %v", err)
	}
	listener.Close()
}

type startupOutput chan string

func (out startupOutput) Write(p []byte) (int, error) {
	out <- string(p)
	return len(p), nil
}

func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_LISTEN_ADDR", "")
	t.Setenv("GEKO_DB_PATH", filepath.Join(dir, "geko.db"))
	var out bytes.Buffer
	if err := run(t.Context(), []string{"backup"}, &out); err == nil {
		t.Fatal("backup without a target succeeded")
	}
	if err := run(t.Context(), []string{"backup", "copy.db"}, &out); err == nil || !strings.Contains(err.Error(), "no Geko database") {
		t.Fatalf("backup of a missing database: %v", err)
	}
	db, err := sqlite.Open(t.Context(), filepath.Join(dir, "geko.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := run(t.Context(), []string{"backup", "copy.db"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), filepath.Join(dir, "copy.db")) {
		t.Fatalf("output %q does not name the backup", out.String())
	}
	if err := run(t.Context(), []string{"backup", "copy.db"}, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("backup overwrote a file: %v", err)
	}
}
