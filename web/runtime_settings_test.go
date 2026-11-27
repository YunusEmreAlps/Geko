package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestLiveSettingsAuthorizationCSRFAndTraceRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geko.yaml")
	data := []byte("# Settings test\nretention_days: 7\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	runtimeSettings := config.NewRuntime(config.Config{Source: config.Source{FilePath: path, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data)), TraceLinkURL: config.SettingSource{Kind: "default"}, TraceLinkName: config.SettingSource{Kind: "default"}}})
	newToken := func() string {
		var value [32]byte
		if _, err := rand.Read(value[:]); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(value[:])
	}
	admin, viewer, triager := newToken(), newToken(), newToken()
	token, err := access.ParseToken(admin)
	if err == nil {
		token, err = token.WithReadTokens("viewer=" + viewer)
	}
	if err == nil {
		token, err = token.WithTriageTokens("triager=" + triager)
	}
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(dir, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /test", StartTime: now, EndTime: now.Add(time.Millisecond), Kind: telemetry.KindServer}}); err != nil {
		t.Fatal(err)
	}
	var auditBuffer bytes.Buffer
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Runtime: runtimeSettings, Tokens: token, Audit: audit.New(&auditBuffer)}); err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(mux)
	do := func(method, target, credential, body, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		if strings.HasPrefix(target, "/api/") {
			r.Header.Set("Content-Type", "application/json")
		} else {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	before := runtimeSettings.Snapshot()
	payload := func(revision, link string) string {
		b, _ := json.Marshal(map[string]string{"revision": revision, "trace_link_url": link, "trace_link_name": "Viewer"})
		return string(b)
	}
	for _, credential := range []string{viewer, triager} {
		for _, endpoint := range []string{"/api/v1/settings/operations", "/api/v1/settings/operations/preview"} {
			if w := do("GET", "/api/v1/settings/operations", credential, "", ""); w.Code != 403 {
				t.Fatal("nonadmin operational read")
			}
			if w := do("POST", endpoint, credential, `{}`, ""); w.Code != 403 && w.Code != 401 {
				t.Fatal("nonadmin operational write")
			}
		}

		scopeName, scopeRole := "viewer", "viewer"
		if credential == triager {
			scopeName, scopeRole = "triager", "triager"
		}
		if w := do("GET", "/traces", credential, "", ""); !strings.Contains(w.Body.String(), `data-workspace-scope="token:`+scopeName+`:`+scopeRole+`"`) || strings.Contains(w.Body.String(), credential) {
			t.Fatal("local workspace identity scope missing or leaked credential")
		}

		if w := do("GET", "/api/v1/settings/budget", credential, "", ""); w.Code != http.StatusForbidden {
			t.Fatalf("nonadmin budget read: %d", w.Code)
		}
		if w := do("POST", "/api/v1/settings/budget", credential, `{"revision":"`+before.Revision+`","limits":{}}`, ""); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("nonadmin budget write: %d", w.Code)
		}

		if w := do("GET", "/api/v1/settings/owners", credential, "", ""); w.Code != http.StatusForbidden {
			t.Fatalf("nonadmin ownership read: %d", w.Code)
		}
		if w := do("POST", "/api/v1/settings/owners", credential, `{"revision":"`+before.Revision+`","owners":{}}`, ""); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("nonadmin ownership write: %d", w.Code)
		}
		if w := do("GET", "/api/v1/settings/runtime", credential, "", ""); w.Code != http.StatusForbidden {
			t.Fatalf("nonadmin settings read: %d", w.Code)
		}
		if w := do("POST", "/api/v1/settings/runtime", credential, payload(before.Revision, "https://viewer.example/{trace_id}"), ""); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("nonadmin settings write: %d", w.Code)
		}
		if w := do("GET", "/settings", credential, "", ""); strings.Contains(w.Body.String(), `action="/settings/runtime"`) || strings.Contains(w.Body.String(), `action="/settings/owners"`) || strings.Contains(w.Body.String(), `action="/settings/budget"`) || strings.Contains(w.Body.String(), `action="/settings/operations/preview"`) {
			t.Fatal("nonadmin sees live settings editor")
		}
	}
	for _, endpoint := range []string{"/api/v1/settings/operations", "/api/v1/settings/operations/preview"} {
		if w := do("POST", endpoint, admin, `{}`, "https://evil.example"); w.Code != 403 {
			t.Fatal("operational CSRF")
		}
	}
	if w := do("POST", "/api/v1/settings/runtime", admin, payload(before.Revision, "https://viewer.example/{trace_id}"), "https://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin update: %d", w.Code)
	}
	if runtimeSettings.Snapshot() != before {
		t.Fatal("rejected requests mutated settings")
	}
	if w := do("POST", "/api/v1/settings/budget", admin, `{"revision":"`+before.Revision+`","limits":{}}`, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin budget: %d", w.Code)
	}
	if w := do("POST", "/api/v1/settings/owners", admin, `{"revision":"`+before.Revision+`","owners":{}}`, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin ownership: %d", w.Code)
	}
	if w := do("POST", "/api/v1/settings/runtime", admin, `{"revision":"`+before.Revision+`"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatal("omitted fields could implicitly clear settings")
	}
	if w := do("POST", "/api/v1/settings/runtime", admin, payload(before.Revision, "https://user:do-not-log@viewer.example/{trace_id}"), ""); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "do-not-log") {
		t.Fatal("invalid URL was accepted or echoed")
	}
	if w := do("POST", "/api/v1/settings/runtime", admin, `{"revision":"`+before.Revision+`","retention_days":1}`, ""); w.Code != http.StatusBadRequest {
		t.Fatal("unsupported live setting was accepted")
	}
	w := do("GET", "/api/v1/settings/runtime", admin, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), before.Revision) || strings.Contains(w.Body.String(), admin) {
		t.Fatalf("admin state: %d %s", w.Code, w.Body)
	}
	w = do("POST", "/api/v1/settings/runtime", admin, payload(before.Revision, "https://viewer.example/{trace_id}"), "")
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	tracePath := "/traces/01" + strings.Repeat("0", 30)
	w = do("GET", tracePath, admin, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `https://viewer.example/01`+strings.Repeat("0", 30)) {
		t.Fatal("existing dashboard did not use updated link")
	}
	if w := do("POST", "/api/v1/settings/runtime", admin, payload(before.Revision, "https://stale.example/{trace_id}"), ""); w.Code != http.StatusConflict {
		t.Fatalf("stale edit: %d", w.Code)
	}
	state := runtimeSettings.Snapshot()
	form := url.Values{"revision": {state.Revision}, "trace_link_url": {"https://second.example/{trace_id}"}, "trace_link_name": {"Second viewer"}}
	w = do("POST", "/settings/runtime", admin, form.Encode(), "http://127.0.0.1")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings#runtime-settings" {
		t.Fatalf("form update: %d %s", w.Code, w.Body)
	}
	ownerPayload := `{"revision":"` + runtimeSettings.Snapshot().Revision + `","owners":{"services":{"api":"private-team-marker"}}}`
	if w := do("POST", "/api/v1/settings/owners", admin, ownerPayload, ""); w.Code != 200 {
		t.Fatalf("ownership save: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/failures?view=groups", admin, "", ""); w.Code != 200 {
		t.Fatal("owner update broke group view")
	}
	for _, secret := range []string{admin, "do-not-log", "viewer.example", "second.example", "stale.example", "private-team-marker"} {
		if strings.Contains(auditBuffer.String(), secret) {
			t.Fatalf("audit leaked settings value %q", secret)
		}
	}
	if !strings.Contains(auditBuffer.String(), "settings.runtime.update") || !strings.Contains(auditBuffer.String(), "conflict") {
		t.Fatal("missing safe update audit")
	}
	// Read-only instance refuses even an administrator.
	readOnlyMux := http.NewServeMux()
	if err := Register(readOnlyMux, store, Settings{Runtime: runtimeSettings, Tokens: token, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/settings/runtime", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer "+admin)
	w = httptest.NewRecorder()
	token.Protect(readOnlyMux).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("read-only instance accepted settings write")
	}
	// Updating the control plane must not race with trace ingestion or a
	// dashboard rendering the current link from the same live manager.
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 20 {
			span := telemetry.Span{TraceID: telemetry.TraceID{byte(i + 2)}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "background", StartTime: now, EndTime: now.Add(time.Millisecond)}
			if err := store.WriteSpans(t.Context(), now, []telemetry.Span{span}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 20 {
			s := runtimeSettings.Snapshot()
			if _, err := runtimeSettings.Update(t.Context(), s.Revision, "https://concurrent.example/{trace_id}", "Concurrent"); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 20 {
			if w := do("GET", tracePath, admin, "", ""); w.Code != http.StatusOK {
				t.Errorf("trace read during update: %d", w.Code)
				return
			}
		}
	})
	wg.Wait()
}
