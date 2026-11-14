package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fileAccess = "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8"
	fileIngest = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	fileRead   = "f0e1d2c3b4a5968778695a4b3c2d1e0f"
	rotated    = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b"
)

func writeTokens(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeTokens(t, path, "access: "+fileAccess+"\ningest:\n  storefront: "+fileIngest+"\nread:\n  agent: "+fileRead+"\ntriage:\n  qa: "+rotated+"\n")
	token, err := LoadTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !token.Bearer("Bearer "+fileAccess) || !token.IngestBearer("Bearer "+fileIngest) || !token.allows(fileRead, ScopeRead) {
		t.Fatal("file tokens not accepted")
	}
	if principal, ok := token.identify(rotated); !ok || principal != (Principal{"qa", RoleTriager, "token"}) {
		t.Fatalf("triage token = %+v", principal)
	}
	if strings.Join(token.Names(ScopeIngest), ",") != "storefront" || strings.Join(token.Names(ScopeRead), ",") != "agent" {
		t.Fatalf("names = %v, %v", token.Names(ScopeIngest), token.Names(ScopeRead))
	}
	for name, content := range map[string]string{
		"empty":          "",
		"no access":      "ingest:\n  storefront: " + fileIngest + "\n",
		"unknown key":    "access: " + fileAccess + "\nwrite:\n  x: " + fileRead + "\n",
		"weak access":    "access: " + strings.Repeat("ab", 16) + "\n",
		"reused token":   "access: " + fileAccess + "\nread:\n  agent: " + fileAccess + "\n",
		"bad name":       "access: " + fileAccess + "\nread:\n  \"a b\": " + fileRead + "\n",
		"shared name":    "access: " + fileAccess + "\nread:\n  qa: " + fileRead + "\ntriage:\n  qa: " + rotated + "\n",
		"invalid syntax": "access: [" + fileAccess + "\n",
	} {
		writeTokens(t, path, content)
		_, err := LoadTokenFile(path)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), fileAccess) || strings.Contains(err.Error(), fileRead) {
			t.Errorf("%s: error reveals a token: %v", name, err)
		}
	}
	if _, err := LoadTokenFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("accepted a missing file")
	}
}

func TestReloadUpdatesCopiesAndEndsSessions(t *testing.T) {
	token, err := ParseToken(fileAccess)
	if err != nil {
		t.Fatal(err)
	}
	copied := token
	handler := copied.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	signIn := httptest.NewRecorder()
	if _, ok := token.SignIn(signIn, httptest.NewRequest(http.MethodPost, "/login", nil), fileAccess); !ok {
		t.Fatal("sign-in failed")
	}
	cookie := signIn.Result().Cookies()[0]
	request := func(auth string, withCookie bool) int {
		r := httptest.NewRequest(http.MethodGet, "http://geko.example/settings", nil)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		if withCookie {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if request("", true) != http.StatusNoContent {
		t.Fatal("session not accepted before reload")
	}
	next, err := ParseToken(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if err := token.Reload(next); err != nil {
		t.Fatal(err)
	}
	if request(fileAccess, false) != http.StatusUnauthorized || request(rotated, false) != http.StatusNoContent {
		t.Fatal("copy of the token did not see the reload")
	}
	if request("", true) != http.StatusUnauthorized {
		t.Fatal("session survived a token rotation")
	}
	if err := token.Reload(Token{}); err == nil {
		t.Fatal("reload turned protection off")
	}
	if err := (Token{}).Reload(next); err == nil {
		t.Fatal("reload turned protection on")
	}
}

func TestWatchTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	writeTokens(t, path, "access: "+fileAccess+"\n")
	token, err := LoadTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A change made before the watcher starts must still be applied.
	writeTokens(t, path, "access: "+fileAccess+"\nread:\n  agent: "+fileRead+"\n")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchTokenFile(ctx, path, token, 10*time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	waitFor := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("token file change not applied")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(func() bool { return token.allows(fileRead, ScopeRead) })
	session := token.state.sessions.create(Principal{Name: accessName, Role: RoleAdmin})
	// An invalid file keeps the tokens in use.
	writeTokens(t, path, "access: short\n")
	time.Sleep(100 * time.Millisecond)
	if !token.matches(fileAccess) {
		t.Fatal("invalid file replaced the tokens")
	}
	// Restoring the same tokens is not a rotation and keeps sessions.
	writeTokens(t, path, "access: "+fileAccess+"\nread:\n  agent: "+fileRead+"\n")
	time.Sleep(100 * time.Millisecond)
	if _, ok := token.state.sessions.lookup(session); !ok {
		t.Fatal("unchanged tokens ended a session")
	}
	writeTokens(t, path, "access: "+rotated+"\n")
	waitFor(func() bool { return token.matches(rotated) && !token.matches(fileAccess) })
}
