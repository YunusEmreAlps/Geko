package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestBrowserSessions(t *testing.T) {
	const admin, read = "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8", "b7e4c2a83f9c1e7a0b5d48e2c6a1f0d9"
	token, err := access.ParseToken(admin)
	if err == nil {
		token, err = token.WithReadTokens("viewer=" + read)
	}
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{AccessToken: true, SignIn: token.SignIn, SignOut: token.SignOut, Tokens: token}); err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(mux)
	do := func(method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, "https://geko.example.com"+target, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, "https://geko.example.com"+target, nil)
		}
		r.Header.Set("Accept", "text/html")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	signIn := func(secret, next string) (*httptest.ResponseRecorder, *http.Cookie) {
		t.Helper()
		w := do(http.MethodPost, "/login", url.Values{"token": {secret}, "next": {next}}, nil)
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "geko_session" {
				return w, cookie
			}
		}
		return w, nil
	}

	w := do(http.MethodGet, "/settings?x=1", nil, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2Fsettings%3Fx%3D1" {
		t.Fatalf("unauthenticated page = %d, %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(http.MethodGet, "/login?next=/settings", nil, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="next" value="/settings"`) {
		t.Fatalf("sign-in page = %d", w.Code)
	}
	if w := do(http.MethodGet, "/assets/dashboard.css", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("sign-in assets = %d", w.Code)
	}
	if w, cookie := signIn("wrong-token-wrong-token-wrong-tok", "/settings"); w.Code != http.StatusUnauthorized || cookie != nil || !strings.Contains(w.Body.String(), "isn&#39;t valid") {
		t.Fatalf("wrong token = %d, cookie %v", w.Code, cookie)
	}

	w, viewer := signIn(read, "//evil.example/")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/failures" || viewer == nil {
		t.Fatalf("read sign-in = %d, %q", w.Code, w.Header().Get("Location"))
	}
	if !viewer.HttpOnly || !viewer.Secure || viewer.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %+v", viewer)
	}
	if w := do(http.MethodGet, "/settings", nil, viewer); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sign out") {
		t.Fatalf("read session page = %d", w.Code)
	}
	if w := do(http.MethodPost, "/groups/state", url.Values{"group": {"x"}}, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("read session change = %d", w.Code)
	}
	if w := do(http.MethodGet, "/applications", nil, viewer); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `action="/applications"`) {
		t.Fatalf("read session sees changes it can't make: %d", w.Code)
	}

	_, owner := signIn(admin, "/settings")
	if w := do(http.MethodGet, "/applications", nil, owner); !strings.Contains(w.Body.String(), `action="/applications" method="post"`) {
		t.Fatal("full session lost the mapping form")
	}
	if w := do(http.MethodPost, "/groups/state", url.Values{"group": {"invalid"}, "state": {"resolved"}}, owner); w.Code != http.StatusBadRequest {
		t.Fatalf("full session change reached the handler with %d", w.Code)
	}
	if w := do(http.MethodPost, "/logout", url.Values{}, owner); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("sign out = %d, %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(http.MethodGet, "/settings", nil, owner); w.Code != http.StatusSeeOther {
		t.Fatalf("revoked session = %d", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "https://geko.example.com/api/v1/failures", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("API without credentials = %d, %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
}
