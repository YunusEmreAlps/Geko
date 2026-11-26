package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runtimeFixture(t *testing.T, yaml string) (*Runtime, string) {
	t.Helper()
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "geko.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEKO_CONFIG", path)
	t.Setenv("GEKO_TRACE_LINK_URL", "")
	t.Setenv("GEKO_TRACE_LINK_NAME", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return NewRuntime(cfg), path
}

func TestRuntimeCommentOnlyYAML(t *testing.T) {
	r, path := runtimeFixture(t, "# Keep this note\n")
	s := r.Snapshot()
	if _, err := r.Update(t.Context(), s.Revision, "https://example/{trace_id}", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Keep this note") {
		t.Fatal("lost comment-only document")
	}
}

func TestRuntimeSavePersistsCommentsAndSurvivesRestart(t *testing.T) {
	r, path := runtimeFixture(t, "# Local tool\nretention_days: 9 # keep history\ntrace_link_url: '' # external viewer\ntrace_link_name: ''\n")
	before := r.Snapshot()
	state, err := r.Update(t.Context(), before.Revision, "https://tempo.example/trace/{trace_id}", "Trace viewer")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == before.Revision || state.AppliedAt.IsZero() || state.URLSource.Kind != "yaml" {
		t.Fatalf("bad applied snapshot: %+v", state)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# Local tool", "# keep history", "# external viewer", "retention_days: 9"} {
		if !strings.Contains(string(data), text) {
			t.Errorf("lost %q", text)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewRuntime(cfg).Snapshot()
	if restarted.URL != state.URL || restarted.Name != state.Name || restarted.Revision != state.Revision || cfg.RetentionDays != 9 {
		t.Fatalf("restart differs: %+v", restarted)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("update left a lock")
	}
}

func TestRuntimeRejectsInvalidStaleAndExternalChanges(t *testing.T) {
	r, path := runtimeFixture(t, "retention_days: 7\n")
	before := r.Snapshot()
	for _, link := range []string{"javascript:alert(1)", "https://user:secret@example/trace/{trace_id}", "https://example/no-placeholder"} {
		if _, err := r.Update(t.Context(), before.Revision, link, "Viewer"); !errors.Is(err, ErrSettingsInvalid) {
			t.Fatalf("invalid accepted: %v", err)
		}
	}
	if r.Snapshot() != before {
		t.Fatal("validation changed live values")
	}
	if _, err := r.Update(t.Context(), "stale", "", ""); !errors.Is(err, ErrSettingsConflict) {
		t.Fatal(err)
	}
	external := []byte("retention_days: 10\n")
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(t.Context(), before.Revision, "", ""); !errors.Is(err, ErrSettingsConflict) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(external) || r.Snapshot() != before {
		t.Fatal("external YAML or last-good values were overwritten")
	}
}

func TestRuntimeEnvironmentLocksAndFailureRollback(t *testing.T) {
	_, path := runtimeFixture(t, "trace_link_url: https://yaml.example/{trace_id}\ntrace_link_name: YAML\n")
	t.Setenv("GEKO_TRACE_LINK_URL", "https://env.example/{trace_id}")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(cfg)
	before := r.Snapshot()
	if before.URLSource.Kind != "env" {
		t.Fatal("env source missing")
	}
	if _, err := r.Update(t.Context(), before.Revision, "https://other.example/{trace_id}", "Other"); !errors.Is(err, ErrSettingsLocked) {
		t.Fatal(err)
	}
	state, err := r.Update(t.Context(), before.Revision, before.URL, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "https://yaml.example/{trace_id}") || strings.Contains(string(data), "https://env.example/") || state.Name != "Renamed" {
		t.Fatal("env value persisted or unlocked name not changed")
	}
	before = state
	original := string(data)
	r.replace = func(string, string) (string, error) { return "", errors.New("disk unavailable") }
	if _, err := r.Update(t.Context(), before.Revision, before.URL, "Failure"); !errors.Is(err, ErrSettingsSave) {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != original || r.Snapshot() != before {
		t.Fatal("failed save changed disk/live settings")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".geko-config-*"))
	if len(files) != 0 {
		t.Fatal("temporary configuration left behind")
	}
}

func TestRuntimeConcurrentSnapshotsAndEdits(t *testing.T) {
	r, _ := runtimeFixture(t, "retention_days: 7\n")
	before := r.Snapshot()
	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for range 12 {
		wg.Go(func() {
			for range 100 {
				_ = r.Snapshot()
			}
			_, err := r.Update(t.Context(), before.Revision, "https://example/{trace_id}", "Viewer")
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrSettingsConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("%d concurrent saves succeeded, want 1", successes)
	}
}

func TestRuntimeReadOnlyFilesAndUnsupportedYAML(t *testing.T) {
	for _, data := range []string{"trace_link_url: &viewer https://example/{trace_id}\n", ""} {
		r, path := runtimeFixture(t, data)
		if data != "" {
			if r.Snapshot().Editable {
				t.Fatal("shared YAML scalar was editable")
			}
			continue
		}
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		before := r.Snapshot()
		if _, err := r.Update(t.Context(), before.Revision, "", ""); !errors.Is(err, ErrSettingsSave) {
			t.Fatal(err)
		}
		if r.Snapshot() != before {
			t.Fatal("read-only save changed live state")
		}
	}
}

func TestRuntimeCancelledUpdateAndWriterLock(t *testing.T) {
	r, path := runtimeFixture(t, "retention_days: 7\n")
	before := r.Snapshot()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Update(ctx, before.Revision, "", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(t.Context(), before.Revision, "", ""); !errors.Is(err, ErrSettingsSave) {
		t.Fatal(err)
	}
	if r.Snapshot() != before {
		t.Fatal("cancel/lock changed state")
	}
}

func TestRuntimeStartupAndSymlinkGuards(t *testing.T) {
	r, path := runtimeFixture(t, "retention_days: 7\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReadOnly = true
	if NewRuntime(cfg).Snapshot().Editable {
		t.Fatal("read-only config enabled edits")
	}
	cfg.ReadOnly = false
	if err := os.WriteFile(path, []byte("retention_days: 8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if NewRuntime(cfg).Snapshot().Editable {
		t.Fatal("startup file change enabled stale edits")
	}
	link := filepath.Join(filepath.Dir(path), "linked.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Skip("symlinks unavailable: ", err)
	}
	cfg.Source.FilePath = link
	if NewRuntime(cfg).Snapshot().Editable {
		t.Fatal("symlinked configuration was editable")
	}
	if r.Snapshot().URL != "" {
		t.Fatal("guard checks changed an existing manager")
	}
}

func TestRuntimeLargeConfigurationRemainsLoadable(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "large.yaml")
	if err := os.WriteFile(path, []byte("#"+strings.Repeat("x", maxRuntimeConfigBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEKO_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("large existing config no longer loads: %v", err)
	}
	state := NewRuntime(cfg).Snapshot()
	if state.Editable || !strings.Contains(state.Reason, "1 MiB") {
		t.Fatal("oversized configuration enabled live editing")
	}
}
