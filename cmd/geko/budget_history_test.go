package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
)

func TestServerBudgetHistorySurvivesGracefulRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cfg := config.Config{ListenAddr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "history.db"), MaxDBMiB: 256, TelemetryBudget: budget.Config{Enabled: true, HistoryDays: 7}}
	start := func() (string, func()) {
		running, stop := context.WithCancel(ctx)
		out := make(startupOutput, 3)
		done := make(chan error, 1)
		go func() { done <- serve(running, cfg, out, serveOptions{}) }()
		base := listening(t, ctx, out, done)
		return base, func() {
			stop()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("shutdown: %v", err)
				}
			case <-ctx.Done():
				t.Error(ctx.Err())
			}
		}
	}
	base, stop := start()
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	payload := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"01000000000000000000000000000000","spanId":"0100000000000000","name":"request","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`
	response, err := client.Post(base+"/v1/traces", "application/json", strings.NewReader(payload))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		stop()
		t.Fatalf("export %d", response.StatusCode)
	}
	stop()
	hour := time.Now().UTC().Truncate(time.Hour)
	base, stop = start()
	defer stop()
	var total int64
	for _, selected := range []time.Time{hour, hour.Add(-time.Hour)} {
		response, err = client.Get(base + "/api/v1/budget?hour=" + selected.Format(time.RFC3339))
		if err != nil {
			t.Fatal(err)
		}
		var report budget.Report
		err = json.NewDecoder(response.Body).Decode(&report)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		total += report.Total.Spans
		if report.Total.Spans > 0 && (!report.Partial || report.HistoryDays != 7) {
			t.Fatalf("restarted budget %+v", report)
		}
	}
	if total != 1 {
		t.Fatalf("restart counted %d spans", total)
	}
}

func TestBudgetCLIRejectsIgnoredHourAndHandlesMissingDeltaPair(t *testing.T) {
	t.Setenv("GEKO_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"available":true,"window_start":"2026-11-24T08:00:00Z","window_end":"2026-11-24T09:00:00Z","span_delta":1,"services":[{"service":"api","span_delta":1}]}`)
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := budgetCommand(t.Context(), []string{"--server", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if err := budgetCommand(t.Context(), []string{"--server", server.URL, "--hour", "2026-11-24T09:00:00Z"}, &out); err == nil {
		t.Fatal("a server ignoring --hour was accepted")
	}
}
