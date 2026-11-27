package web

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestRuntimeOwnersAdminAPIAndSettings(t *testing.T) {
	data := []byte("owners:\n  services:\n    api: before-team\n")
	file := filepath.Join(t.TempDir(), "geko.yaml")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := config.NewRuntime(config.Config{Source: config.Source{FilePath: file, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data))}})
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "owners.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest("GET", "http://127.0.0.1/settings", nil))
	if !strings.Contains(get.Body.String(), `action="/settings/owners"`) {
		t.Error("missing ownership editor")
	}
	body := `{"revision":"` + r.Snapshot().Revision + `","owners":{"services":{"api":"after-team"},"applications":{}}}`
	request := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/settings/owners", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != 200 || r.Owners().For("", "api") != "after-team" {
		t.Fatalf("save owners %d %s", w.Code, w.Body)
	}
}
