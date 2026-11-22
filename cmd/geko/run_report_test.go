package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportDistinguishesUnknownFromZero(t *testing.T) {
	const runID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, body             string
		status, downloadStatus int
		available              bool
	}{
		{"zero", `{"run_id":"` + runID + `","items":[]}`, 200, 200, true},
		{"query unavailable", "unavailable", 503, 200, false},
		{"malformed", "{", 200, 200, false},
		{"unsupported run filter", `{"items":[]}`, 200, 200, false},
		{"invalid trace ID", `{"run_id":"` + runID + `","items":[{"trace_id":"../../escape"}]}`, 200, 200, false},
		{"download unavailable", `{"run_id":"` + runID + `","items":[{"trace_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`, 200, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/failures" {
					w.WriteHeader(tc.status)
					w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(tc.downloadStatus)
				w.Write([]byte(`{}`))
			}))
			defer server.Close()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "failures"), []byte("0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := writeRunReport(t.Context(), server.URL, "", runID, dir)
			if (err == nil) != tc.available {
				t.Fatalf("writeRunReport = %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var status runReportStatus
			if err := json.Unmarshal(data, &status); err != nil {
				t.Fatal(err)
			}
			if status.RunID != runID {
				t.Fatalf("status = %+v", status)
			}
			if tc.available {
				if status.TelemetryStatus != "available" || status.Failures == nil || *status.Failures != 0 {
					t.Fatalf("zero result = %+v", status)
				}
			} else {
				if status.TelemetryStatus != "unavailable" || status.Failures != nil {
					t.Fatalf("unknown result = %+v", status)
				}
				if _, err := os.Stat(filepath.Join(dir, "failures")); !os.IsNotExist(err) {
					t.Fatalf("stale count remains: %v", err)
				}
				markdown, err := os.ReadFile(filepath.Join(dir, "report.md"))
				if err != nil || !strings.Contains(string(markdown), "count is unknown") {
					t.Fatalf("summary = %s, %v", markdown, err)
				}
			}
		})
	}
}
