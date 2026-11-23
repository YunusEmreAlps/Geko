package sqlite

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

const baselineRun = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const candidateRun = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func runSpans(id byte, run, kind, environment, version string) []telemetry.Span {
	root := testSpan(id, 1, "api", 200)
	root.ResourceAttributes = telemetry.Attributes{"service.namespace": "Shop", "deployment.environment.name": environment, "service.version": version, "geko.run.id": run}
	spans := []telemetry.Span{root}
	if kind != "" {
		child := testSpan(id, 2, "worker", 503)
		child.ParentID = root.ID
		child.Name = "call <gateway>"
		child.ResourceAttributes = root.ResourceAttributes
		child.Events = []telemetry.Event{{Name: "exception", Time: child.StartTime, Attributes: telemetry.Attributes{"exception.type": kind, "exception.message": "do not copy free text", "exception.stacktrace": "private-stack"}}}
		spans = append(spans, child)
	}
	return spans
}

func TestCompareRunsObservedEvidenceAndRemovedHistory(t *testing.T) {
	store := openTestStore(t)
	var spans []telemetry.Span
	for i, kind := range []string{"Shared", "Gone", ""} {
		spans = append(spans, runSpans(byte(i+1), baselineRun, kind, "test", "1")...)
	}
	for i, kind := range []string{"Shared", "New", ""} {
		spans = append(spans, runSpans(byte(i+4), candidateRun, kind, "test", "2")...)
	}
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	left, err := store.RunSnapshot(t.Context(), baselineRun)
	if err != nil || left.Traces != 3 || left.TaggedSpans != 5 || left.FailingTraces != 2 || len(left.Groups) != 2 {
		t.Fatalf("snapshot %+v, %v", left, err)
	}
	data, _ := json.Marshal(left)
	if strings.Contains(string(data), "private-stack") || strings.Contains(string(data), "do not copy") {
		t.Fatal("snapshot exposes free-text evidence")
	}
	result, err := store.CompareRuns(t.Context(), baselineRun, candidateRun)
	if err != nil || result.Status != "partial" || len(result.Groups) != 3 {
		t.Fatalf("comparison %+v, %v", result, err)
	}
	want := map[string]string{"Shared": "recurring", "Gone": "not_observed", "New": "newly_observed"}
	for _, change := range result.Groups {
		if change.State != want[change.Group.ExceptionType] {
			t.Fatalf("change %+v", change)
		}
	}
	right := result.Candidate
	if _, err := store.db.Exec(`DELETE FROM traces WHERE trace_id IN (SELECT trace_id FROM spans WHERE json_extract(resource_attributes,'$."geko.run.id"')=?)`, baselineRun); err != nil {
		t.Fatal(err)
	}
	result, err = store.CompareRuns(t.Context(), baselineRun, candidateRun)
	if err != nil || result.Status != "unavailable" || len(result.Groups) != 0 {
		t.Fatalf("removed history %+v, %v", result, err)
	}
	portable, err := investigation.CompareRunSnapshots(left, right)
	if err != nil || len(portable.Groups) != 3 || portable.Status != "partial" {
		t.Fatalf("captured evidence lost after deletion: %+v, %v", portable, err)
	}
}

func TestCompareRunsDoesNotInferFixFromMissingTagsOrDifferentScope(t *testing.T) {
	for _, scenario := range []string{"no-export", "missing-run", "different-environment", "healthy-candidate"} {
		t.Run(scenario, func(t *testing.T) {
			store := openTestStore(t)
			spans := runSpans(1, baselineRun, "Timeout", "test", "1")
			switch scenario {
			case "missing-run":
				spans = append(spans, runSpans(2, "", "", "test", "2")...)
			case "different-environment":
				spans = append(spans, runSpans(2, candidateRun, "", "production", "2")...)
			case "healthy-candidate":
				spans = append(spans, runSpans(2, candidateRun, "", "test", "2")...)
			}
			if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
				t.Fatal(err)
			}
			result, err := store.CompareRuns(t.Context(), baselineRun, candidateRun)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "healthy-candidate" {
				if result.Status != "partial" || len(result.Groups) != 1 || result.Groups[0].State != "not_observed" {
					t.Fatalf("healthy comparison %+v", result)
				}
			} else if result.Status != "unavailable" || len(result.Groups) != 0 {
				t.Fatalf("unsafe comparison %+v", result)
			}
		})
	}
}

func TestRunSnapshotBoundsAndReadOnly(t *testing.T) {
	store := openTestStore(t)
	var spans []telemetry.Span
	for i := range investigation.RunSnapshotLimit + 1 {
		spans = append(spans, runSpans(byte(i+1), baselineRun, fmt.Sprintf("Error%d", i), "test", "1")...)
	}
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	snapshot, err := reader.RunSnapshot(t.Context(), baselineRun)
	if err != nil || !snapshot.Truncated || len(snapshot.Groups) != investigation.RunSnapshotLimit || snapshot.FailingTraces != 101 {
		t.Fatalf("bounded snapshot %+v, %v", snapshot, err)
	}
	if _, err := reader.RunSnapshot(t.Context(), "../invalid"); err == nil {
		t.Fatal("invalid run ID accepted")
	}
}

func TestRunQueriesUseRunIDIndex(t *testing.T) {
	store := openTestStore(t)
	matching, args := legacyGroupMatches(investigation.FailureFilter{RunID: baselineRun})
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`SELECT count(*),count(DISTINCT trace_id) FROM spans WHERE json_extract(resource_attributes,'$."geko.run.id"')=?`, []any{baselineRun}},
		{matching, args},
	} {
		rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query.sql, query.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), "spans_run_id") || strings.Contains(plan.String(), "SCAN spans") {
			t.Fatalf("run query lost indexed membership:\n%s", &plan)
		}
	}
}
