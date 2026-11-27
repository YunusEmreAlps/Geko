package config

import (
	"errors"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
)

func TestRuntimeBudgetPersistenceAndSharedRevision(t *testing.T) {
	r, path := runtimeFixture(t, "# preserved\ntelemetry_budget:\n  enabled: true\n  history_days: 7 # history stays\n  attributes: [http.route] # keys stay\n  default:\n    spans_per_hour: 9 # limit note\nretention_days: 9\n")
	before := r.BudgetState()
	if !before.Editable || before.Sources.DefaultSpans.Kind != "yaml" || before.Sources.Cardinality.Kind != "default" {
		t.Fatalf("state: %+v", before)
	}
	next := budget.ReportingLimits{Defaults: budget.Limits{Spans: 2}, CardinalityLimit: 3, Services: []budget.ServiceLimit{{Service: "api", Limits: budget.Limits{Bytes: 10}}}}
	state, err := r.UpdateBudget(t.Context(), before.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == before.Revision || r.BudgetPolicy().Limits().Defaults.Spans != 2 {
		t.Fatal("not published")
	}
	if _, err := r.UpdateOwners(t.Context(), before.Revision, OwnerMappings{}); err != ErrSettingsConflict {
		t.Fatalf("stale editor: %v", err)
	}
	data, _ := os.ReadFile(path)
	for _, part := range []string{"# preserved", "history_days: 7 # history stays", "attributes: [http.route] # keys stay", "# limit note", "retention_days: 9"} {
		if !strings.Contains(string(data), part) {
			t.Errorf("lost %q in %s", part, data)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelemetryBudget.Defaults.Spans != 2 || cfg.TelemetryBudget.HistoryDays != 7 || len(cfg.TelemetryBudget.Attributes) != 1 {
		t.Fatalf("reload: %+v", cfg.TelemetryBudget)
	}
}

func TestRuntimeBudgetRollbackAndDisabled(t *testing.T) {
	r, path := runtimeFixture(t, "telemetry_budget:\n  enabled: true\n")
	before := r.BudgetState()
	data, _ := os.ReadFile(path)
	r.replace = func(string, string) (string, error) { return "", errors.New("disk") }
	if _, err := r.UpdateBudget(t.Context(), before.Revision, budget.ReportingLimits{Defaults: budget.Limits{Spans: 1}}); !errors.Is(err, ErrSettingsSave) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(data) != string(after) || r.BudgetState().Revision != before.Revision || r.BudgetPolicy().Limits().Defaults.Spans != 0 {
		t.Fatal("failed save published")
	}
	r.replace = replaceConfigFile
	if _, err := r.UpdateBudget(t.Context(), before.Revision, budget.ReportingLimits{CardinalityLimit: 256}); !errors.Is(err, ErrBudgetInvalid) {
		t.Fatal(err)
	}
}

func TestRuntimeBudgetDisabledAndUnsafeYAML(t *testing.T) {
	for _, text := range []string{"# disabled\n", "telemetry_budget: &budget\n  enabled: true\n"} {
		t.Run(text, func(t *testing.T) {
			r, _ := runtimeFixture(t, text)
			if r.BudgetState().Editable {
				t.Fatal("unsafe or disabled editor")
			}
			if _, err := r.UpdateBudget(t.Context(), r.Snapshot().Revision, budget.ReportingLimits{}); err != ErrSettingsLocked {
				t.Fatal(err)
			}
		})
	}
	for _, text := range []string{"enabled: true", "default: {spans_per_hour: -1}", "services: &s []", "{}\n---\n{}", "cardinality_limit: 256"} {
		if _, err := ParseBudgetLimits([]byte(text)); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}

func TestRuntimeBudgetPreservesServiceRuleComments(t *testing.T) {
	r, path := runtimeFixture(t, "telemetry_budget:\n  enabled: true\n  services:\n    # api comment\n    - namespace: shop\n      service: api # service comment\n      spans_per_hour: 9 # rule comment\n")
	_, err := r.UpdateBudget(t.Context(), r.Snapshot().Revision, budget.ReportingLimits{Services: []budget.ServiceLimit{{Namespace: "shop", Service: "api", Limits: budget.Limits{Spans: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, text := range []string{"# api comment", "# service comment", "# rule comment"} {
		if !strings.Contains(string(data), text) {
			t.Errorf("lost %s", text)
		}
	}
}

func TestRuntimeBudgetSourcesIndependentOfEditability(t *testing.T) {
	for _, text := range []string{"telemetry_budget: &budget\n  enabled: true\n  default: {spans_per_hour: 5}\n  cardinality_limit: 3\n", "read_only: true\ntelemetry_budget:\n  enabled: true\n  default: {spans_per_hour: 5}\n  cardinality_limit: 3\n"} {
		t.Run(text, func(t *testing.T) {
			r, _ := runtimeFixture(t, text)
			state := r.BudgetState()
			if state.Editable || state.Sources.DefaultSpans.Kind != "yaml" || state.Sources.Cardinality.Kind != "yaml" || state.Sources.DefaultBytes.Kind != "default" {
				t.Fatalf("locked sources: %+v", state)
			}
		})
	}
}

func TestBudgetConfigRetainsSharedAlias(t *testing.T) {
	r, _ := runtimeFixture(t, "listen_addr: &shared '127.0.0.1:4318'\ntelemetry_budget:\n  enabled: true\n  services:\n    - service: api\n      namespace: *shared\n      spans_per_hour: 2\n")
	state := r.BudgetState()
	if state.Editable || state.Sources.Services.Kind != "yaml" || len(state.Limits.Services) != 1 || state.Limits.Services[0].Namespace != "127.0.0.1:4318" {
		t.Fatalf("shared alias: %+v", state)
	}
}

func TestRuntimeBudgetConcurrentIngestAndEditors(t *testing.T) {
	r, _ := runtimeFixture(t, "telemetry_budget:\n  enabled: true\n  history_days: 7\n")
	now := time.Now()
	tracker := budget.New(budget.Config{Enabled: true, HistoryDays: 7}, now).WithPolicySource(r.BudgetPolicy)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 30 {
			_, err := r.UpdateBudget(t.Context(), r.Snapshot().Revision, budget.ReportingLimits{Defaults: budget.Limits{Spans: int64(i)}})
			if err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 100 {
			tracker.Record(now, []telemetry.Span{{ServiceName: "api"}}, []budget.Observation{{Span: &trace.Span{}}})
		}
	})
	wg.Go(func() {
		for range 100 {
			tracker.Snapshot(now, false)
			r.BudgetState()
			for _, c := range tracker.Checkpoints(now) {
				if err := c.Validate(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	})
	wg.Wait()
	if tracker.Snapshot(now, false).Total.Spans != 100 {
		t.Fatal("lost measurements during runtime updates")
	}
	revision := r.Snapshot().Revision
	results := make(chan error, 3)
	wg.Go(func() { _, err := r.UpdateBudget(t.Context(), revision, budget.ReportingLimits{}); results <- err })
	wg.Go(func() { _, err := r.UpdateOwners(t.Context(), revision, OwnerMappings{}); results <- err })
	wg.Go(func() {
		_, err := r.Update(t.Context(), revision, "https://example/{trace_id}", "Trace")
		results <- err
	})
	wg.Wait()
	close(results)
	applied, conflicts := 0, 0
	for err := range results {
		if err == nil {
			applied++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if applied != 1 || conflicts != 2 {
		t.Fatalf("shared revision: %d applied, %d conflicts", applied, conflicts)
	}
}
