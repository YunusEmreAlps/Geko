package config

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestRuntimeOwnersPersistAndShareRevisionWithTraceLinks(t *testing.T) {
	r, path := runtimeFixture(t, "# keep root\nowners:\n  services:\n    api: old-team # keep team note\nretention_days: 9 # keep history\n")
	before := r.Snapshot()
	state, err := r.UpdateOwners(t.Context(), before.Revision, OwnerMappings{Services: map[string]string{"api": "new-team"}, Applications: map[string]string{"shop": "shop-team"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == before.Revision || r.Owners().For("shop", "api") != "new-team" || r.Owners().For("shop", "other") != "shop-team" {
		t.Fatal("owners were not applied")
	}
	if _, err := r.Update(t.Context(), before.Revision, "https://example/{trace_id}", ""); err != ErrSettingsConflict {
		t.Fatalf("stale trace-link revision accepted: %v", err)
	}
	data, _ := os.ReadFile(path)
	for _, text := range []string{"# keep root", "# keep team note", "# keep history", "retention_days: 9"} {
		if !strings.Contains(string(data), text) {
			t.Errorf("lost %q", text)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owners.For("shop", "api") != "new-team" {
		t.Fatal("saved rules not loaded after restart")
	}
}

func TestRuntimeOwnersLocksRollbackAndClear(t *testing.T) {
	r, path := runtimeFixture(t, "owners:\n  services:\n    api: old-team\n")
	before := r.Snapshot()
	original, _ := os.ReadFile(path)
	r.replace = func(string, string) (string, error) { return "", errors.New("disk failure") }
	if _, err := r.UpdateOwners(t.Context(), before.Revision, OwnerMappings{Services: map[string]string{"api": "new-team"}}); !errors.Is(err, ErrSettingsSave) {
		t.Fatalf("failed replacement: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) || r.Snapshot() != before || r.Owners().For("", "api") != "old-team" {
		t.Fatal("failed save changed active/persisted rules")
	}
	r.replace = replaceConfigFile
	view := r.OwnersState()
	view.Mappings.Services["api"] = "external-mutation"
	if r.Owners().For("", "api") != "old-team" {
		t.Fatal("API state exposed mutable maps")
	}
	if _, err := r.UpdateOwners(t.Context(), before.Revision, OwnerMappings{}); err != nil {
		t.Fatal(err)
	}
	if len(r.Owners().Rules()) != 0 {
		t.Fatal("clear did not remove rules")
	}
	t.Setenv("GEKO_OWNERS", "service:api=env-team")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	locked := NewRuntime(cfg)
	if locked.OwnersState().Editable || locked.OwnersState().Source.Kind != "env" {
		t.Fatal("environment ownership was not locked")
	}
	if _, err := locked.UpdateOwners(t.Context(), locked.Snapshot().Revision, OwnerMappings{}); err != ErrSettingsLocked {
		t.Fatal("environment rules could be overwritten")
	}
}

func TestRuntimeOwnersConcurrentEditsShareRevision(t *testing.T) {
	r, _ := runtimeFixture(t, "# mixed editors\n")
	before := r.Snapshot()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Go(func() {
		_, err := r.Update(t.Context(), before.Revision, "https://example/{trace_id}", "Viewer")
		results <- err
	})
	wg.Go(func() {
		_, err := r.UpdateOwners(t.Context(), before.Revision, OwnerMappings{Services: map[string]string{"api": "team"}})
		results <- err
	})
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent edits: %d saved, %d conflicted", successes, conflicts)
	}
}

func TestOwnerYAMLValidationAndIndependentEditors(t *testing.T) {
	for _, data := range []string{"services: &s {api: team}\napplications: *s\n", "services: {api: team}\nunknown: x\n", "services: {api: team}\n---\n{}\n", "services: {api: team, api: other}\n", "[not, a, mapping]"} {
		if _, err := ParseOwnerMappings([]byte(data)); !errors.Is(err, ErrOwnersInvalid) {
			t.Errorf("unsafe ownership accepted: %q (%v)", data, err)
		}
	}
	r, _ := runtimeFixture(t, "trace_link_url: &viewer 'https://example/{trace_id}'\n")
	if r.Snapshot().Editable || !r.OwnersState().Editable {
		t.Fatal("trace scalar anchor incorrectly locked ownership editor")
	}
	if _, err := r.UpdateOwners(t.Context(), r.Snapshot().Revision, OwnerMappings{Services: map[string]string{"api": "team"}}); err != nil {
		t.Fatal(err)
	}
	r, _ = runtimeFixture(t, "owners: &teams\n  services: {api: team}\n")
	if r.OwnersState().Editable || !r.Snapshot().Editable {
		t.Fatal("ownership anchor did not independently lock its editor")
	}
}

func TestOwnerPublicationConcurrentWithLookups(t *testing.T) {
	r, _ := runtimeFixture(t, "owners:\n  services: {api: team}\n")
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 20 {
			if _, err := r.UpdateOwners(t.Context(), r.Snapshot().Revision, OwnerMappings{Services: map[string]string{"api": "team"}}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 100 {
			if r.Owners().For("", "api") != "team" {
				t.Error("partial ownership snapshot")
			}
			state := r.OwnersState()
			state.Mappings.Services["api"] = "caller-owned"
		}
	})
	wg.Wait()
	if r.Owners().For("", "api") != "team" {
		t.Fatal("caller mutated published rules")
	}
}
