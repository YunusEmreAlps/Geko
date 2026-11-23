package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func comparisonSnapshot(id string) investigation.RunSnapshot {
	return investigation.RunSnapshot{SchemaVersion: 1, RunID: id, CapturedAt: time.Now().UTC(), CollectionStatus: "available", Traces: 1, TaggedSpans: 1,
		Contexts: []investigation.RunContext{{Application: "Shop", Namespace: "Shop", Service: "api", Environment: "test", Version: "1", Traces: 1}}, Groups: []investigation.RunGroup{}}
}

func TestUnavailableComparisonDoesNotInventZeroCounts(t *testing.T) {
	result := investigation.RunComparison{Status: "unavailable", Baseline: investigation.UnavailableRun(strings.Repeat("a", 32)), Candidate: investigation.UnavailableRun(strings.Repeat("b", 32))}
	text := runComparisonMarkdown(result)
	if strings.Count(text, "trace count unknown") != 2 || strings.Contains(text, "0 retained traces") {
		t.Fatalf("unavailable counts misrepresented: %s", text)
	}
}

func TestCompareRunFilesAndUntrustedArtifacts(t *testing.T) {
	dir := t.TempDir()
	left, right := comparisonSnapshot(strings.Repeat("a", 32)), comparisonSnapshot(strings.Repeat("b", 32))
	if err := saveRunSnapshotFile(dir, left); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "run.json")
	other := t.TempDir()
	if err := saveRunSnapshotFile(other, right); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := compareRunsCommand(t.Context(), []string{"--baseline", baseline, "--candidate", filepath.Join(other, "run.json"), "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var result investigation.RunComparison
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "partial" {
		t.Fatalf("comparison %s", &out)
	}
	for _, data := range []string{`{bad`, strings.Repeat("x", maxRunSnapshotBytes+1), `{"schema_version":999}`} {
		if err := os.WriteFile(baseline, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRunSnapshot(t.Context(), baseline, "", ""); err == nil {
			t.Fatal("untrusted snapshot accepted")
		}
	}
}

func TestRunCapturesBaselineBeforeReusingReportAndKeepsExit(t *testing.T) {
	for _, helper := range []string{"quiet", "exit7"} {
		t.Run(helper, func(t *testing.T) {
			t.Setenv("GEKO_RUN_HELPER", helper)
			t.Setenv("GEKO_TOKEN", "")
			dir := t.TempDir()
			baseline := comparisonSnapshot(strings.Repeat("a", 32))
			if err := saveRunSnapshotFile(dir, baseline); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/failures" {
					json.NewEncoder(w).Encode(map[string]any{"run_id": r.URL.Query().Get("run_id"), "items": []any{}})
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/v1/runs/") {
					json.NewEncoder(w).Encode(comparisonSnapshot(strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")))
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			var stderr bytes.Buffer
			err := runCommand(t.Context(), []string{"--server", server.URL, "--baseline", filepath.Join(dir, "run.json"), "--report", dir, "--", os.Args[0], "-test.run=^TestRunHelperProcess$"}, io.Discard, &stderr)
			var exit exitError
			if helper == "quiet" && err != nil || helper == "exit7" && (!errors.As(err, &exit) || exit != 7) {
				t.Fatalf("child result %v: %s", err, &stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, "comparison.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result investigation.RunComparison
			if json.Unmarshal(data, &result) != nil || result.Status != "partial" || result.Baseline.RunID != baseline.RunID || result.Candidate.RunID == baseline.RunID {
				t.Fatalf("baseline was overwritten: %s", data)
			}
		})
	}
}

func TestCompareRunsThroughAPIAndMCP(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	baseline, candidate := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for i, id := range []string{baseline, candidate} {
		span := telemetry.Span{TraceID: telemetry.TraceID{byte(i + 1)}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /checkout", Kind: telemetry.KindServer,
			StartTime: time.Now(), EndTime: time.Now(), Attributes: telemetry.Attributes{"http.response.status_code": int64(200)},
			ResourceAttributes: telemetry.Attributes{"service.namespace": "Shop", "deployment.environment.name": "test", "geko.run.id": id}}
		if i == 0 {
			span.Events = []telemetry.Event{{Name: "exception", Attributes: telemetry.Attributes{"exception.type": "Timeout"}}}
		}
		if err := store.WriteSpans(t.Context(), time.Now(), []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	api.Register(mux, store)
	server := httptest.NewServer(mux)
	defer server.Close()
	var out bytes.Buffer
	if err := compareRunsCommand(t.Context(), []string{"--server", server.URL, "--baseline", baseline, "--candidate", candidate, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var result investigation.RunComparison
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Groups) != 1 || result.Groups[0].State != "not_observed" {
		t.Fatalf("API comparison %s", &out)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	connection, err := investigationServer(store, ownership.Owners{}).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	callTool(t, session, "compare_runs", map[string]any{"baseline_run_id": baseline, "candidate_run_id": candidate}, &result)
	if result.Status != "partial" || len(result.Groups) != 1 {
		t.Fatalf("MCP comparison %+v", result)
	}
	if got := callTool(t, session, "compare_runs", map[string]any{"baseline_run_id": "invalid", "candidate_run_id": candidate}, nil); !got.IsError {
		t.Fatal("MCP accepted invalid run ID")
	}
}
