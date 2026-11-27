package config

import (
	"errors"
	"os"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/storage"
)

func TestRuntimeOperationsRequireAcknowledgmentAndPersist(t *testing.T) {
	r, path := runtimeFixture(t, "# keep\ntelemetry_budget:\n  enabled: true\n  attributes: [http.route]\nretention_days: 7\n")
	before := r.OperationsState()
	next := before.Policy
	next.RetentionDays = 1
	next.MaxDBMiB = 64
	next.IngestConcurrency = 2
	next.OnFull = storage.Reject
	next.Redaction = "strict"
	next.BudgetHistoryDays = 3
	if _, err := r.UpdateOperations(t.Context(), before.Revision, next, false); !errors.Is(err, ErrOperationsConfirm) {
		t.Fatal(err)
	}
	if r.Operations().RetentionDays != 7 {
		t.Fatal("unconfirmed policy published")
	}
	state, err := r.UpdateOperations(t.Context(), before.Revision, next, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == before.Revision || r.Operations() != next {
		t.Fatal("policy not applied")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionDays != 1 || cfg.TelemetryBudget.HistoryDays != 3 || cfg.IngestSlots != 2 {
		t.Fatal("restart lost policy")
	}
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Fatal("no configuration")
	}
}
func TestRuntimeOperationsEnvLockAndFailure(t *testing.T) {
	r, _ := runtimeFixture(t, "retention_days: 7\n")
	state := r.OperationsState()
	next := state.Policy
	next.IngestConcurrency = 9
	r.replace = func(string, string) (string, error) { return "", errors.New("disk failure") }
	if _, err := r.UpdateOperations(t.Context(), state.Revision, next, true); !errors.Is(err, ErrSettingsSave) {
		t.Fatal(err)
	}
	if r.Operations() != state.Policy {
		t.Fatal("failed save applied")
	}
	t.Setenv("GEKO_RETENTION_DAYS", "10")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	r = NewRuntime(cfg)
	next = r.Operations()
	next.RetentionDays = 2
	if _, err := r.UpdateOperations(t.Context(), r.Snapshot().Revision, next, true); !errors.Is(err, ErrSettingsLocked) {
		t.Fatal(err)
	}
}

func TestHistoryChangesNeedAcknowledgment(t *testing.T) {
	r, _ := runtimeFixture(t, "telemetry_budget: {enabled: true, history_days: 30}\n")
	old := r.Operations()
	next := old
	next.BudgetHistoryDays = 1
	if !next.NeedsConfirmation(old) {
		t.Fatal("history deletion did not require acknowledgment")
	}
	old.BudgetHistoryDays = 0
	if !next.NeedsConfirmation(old) {
		t.Fatal("history persistence did not require acknowledgment")
	}
}

func TestOperationalEditsWithNullDisabledBudget(t *testing.T) {
	r, _ := runtimeFixture(t, "telemetry_budget: null\n")
	next := r.Operations()
	next.IngestConcurrency = 3
	if _, err := r.UpdateOperations(t.Context(), r.Snapshot().Revision, next, false); err != nil {
		t.Fatal(err)
	}
}
