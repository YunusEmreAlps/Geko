package access

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

const (
	adminSecret  = "0123456789abcdef0123456789abcdef"
	readSecret   = "f0e1d2c3b4a5968778695a4b3c2d1e0f"
	triageSecret = "9a8b7c6d5e4f30211203f4e5d6c7b8a9"
)

func roleToken(t *testing.T) Token {
	t.Helper()
	token, err := ParseToken(adminSecret)
	if err == nil {
		token, err = token.WithReadTokens("agent=" + readSecret)
	}
	if err == nil {
		token, err = token.WithTriageTokens("qa=" + triageSecret)
	}
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func callerHandler(got *Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got, _ = Caller(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestTriageTokens(t *testing.T) {
	token := roleToken(t)
	if _, err := token.WithTriageTokens("agent=" + strings.Repeat("abcdef0123456789", 2)); err == nil {
		t.Fatal("a triage token reused a read token's name")
	}
	if _, err := token.WithReadTokens("qa=" + strings.Repeat("abcdef0123456789", 2)); err == nil {
		t.Fatal("a read token reused a triage token's name")
	}
	var caller Principal
	handler := token.Protect(callerHandler(&caller))
	for _, tc := range []struct {
		method, path, secret string
		status               int
		want                 Principal
	}{
		{"GET", "/failures", triageSecret, 204, Principal{"qa", RoleTriager, "token"}},
		{"POST", "/groups/state", triageSecret, 204, Principal{"qa", RoleTriager, "token"}},
		{"POST", "/mcp", triageSecret, 204, Principal{"qa", RoleTriager, "token"}},
		{"POST", "/applications", triageSecret, 401, Principal{}},
		{"POST", "/traces/import", triageSecret, 401, Principal{}},
		{"POST", "/v1/traces", triageSecret, 401, Principal{}},
		{"POST", "/groups/state", readSecret, 401, Principal{}},
		{"GET", "/failures", readSecret, 204, Principal{"agent", RoleViewer, "token"}},
		{"POST", "/applications", adminSecret, 204, Principal{"access", RoleAdmin, "token"}},
	} {
		caller = Principal{}
		r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || caller != tc.want {
			t.Errorf("%s %s = %d %+v, want %d %+v", tc.method, tc.path, w.Code, caller, tc.status, tc.want)
		}
	}
}

func TestSessionKeepsRole(t *testing.T) {
	token := roleToken(t)
	signIn := httptest.NewRecorder()
	principal, ok := token.SignIn(signIn, httptest.NewRequest(http.MethodPost, "/login", nil), triageSecret)
	if !ok || principal != (Principal{"qa", RoleTriager, "session"}) {
		t.Fatalf("sign-in = %+v %v", principal, ok)
	}
	cookie := signIn.Result().Cookies()[0]
	var caller Principal
	handler := token.Protect(callerHandler(&caller))
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/groups/state", 204}, {"POST", "/applications", 403}} {
		r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	if caller != (Principal{"qa", RoleTriager, "session"}) {
		t.Fatalf("caller = %+v", caller)
	}
}

func TestProxyAuth(t *testing.T) {
	roles, err := ParseProxyRoles(" Alice@Example.com=admin, bob@example.com=triager ")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParsePrefixes("10.0.0.0/8, 192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	token, err := roleToken(t).WithProxy(ProxyAuth{Header: "x-auth-request-email", Trusted: trusted, Roles: roles, Default: RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	// Adding tokens afterwards keeps the proxy setting.
	if token, err = token.WithIngestTokens("shop=" + ingestSecret); err != nil || token.Proxy() == nil {
		t.Fatal("proxy setting lost", err)
	}
	var caller Principal
	handler := token.Protect(callerHandler(&caller))
	for _, tc := range []struct {
		name, remote, method, path, user, bearer string
		status                                   int
		want                                     Principal
	}{
		{"listed admin", "10.1.2.3:5000", "POST", "/applications", "alice@example.com", "", 204, Principal{"alice@example.com", RoleAdmin, "proxy"}},
		{"listed triager", "192.0.2.1:5000", "POST", "/groups/state", "bob@example.com", "", 204, Principal{"bob@example.com", RoleTriager, "proxy"}},
		{"triager cannot import", "10.1.2.3:5000", "POST", "/traces/import", "bob@example.com", "", 403, Principal{}},
		{"unlisted viewer", "10.1.2.3:5000", "GET", "/failures", "carol@example.com", "", 204, Principal{"carol@example.com", RoleViewer, "proxy"}},
		{"viewer cannot triage", "10.1.2.3:5000", "POST", "/groups/state", "carol@example.com", "", 403, Principal{}},
		{"untrusted address ignores the header", "203.0.113.9:5000", "GET", "/failures", "alice@example.com", "", 401, Principal{}},
		{"untrusted address falls back to tokens", "203.0.113.9:5000", "GET", "/failures", "alice@example.com", readSecret, 204, Principal{"agent", RoleViewer, "token"}},
		{"trusted proxy without header uses tokens", "10.1.2.3:5000", "GET", "/failures", "", readSecret, 204, Principal{"agent", RoleViewer, "token"}},
		{"ingest ignores the header", "10.1.2.3:5000", "POST", "/v1/traces", "alice@example.com", "", 401, Principal{}},
		{"ingest token through the proxy", "10.1.2.3:5000", "POST", "/v1/traces", "alice@example.com", ingestSecret, 204, Principal{}},
		{"control characters", "10.1.2.3:5000", "GET", "/failures", "eve\x01", "", 403, Principal{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller = Principal{}
			r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Accept", "application/json")
			if tc.user != "" {
				r.Header.Set("X-Auth-Request-Email", tc.user)
			}
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || caller != tc.want {
				t.Fatalf("= %d %+v, want %d %+v", w.Code, caller, tc.status, tc.want)
			}
		})
	}

	refusing, err := roleToken(t).WithProxy(ProxyAuth{Header: "X-Forwarded-User", Trusted: trusted, Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://geko.example/failures", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("X-Forwarded-User", "mallory")
	w := httptest.NewRecorder()
	refusing.Protect(callerHandler(&caller)).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlisted user without a default role = %d", w.Code)
	}
}

func TestProxyAuthValidation(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for _, tc := range []struct {
		name  string
		token Token
		proxy ProxyAuth
	}{
		{"no access token", Token{}, ProxyAuth{Header: "X-User", Trusted: trusted}},
		{"no trusted addresses", roleToken(t), ProxyAuth{Header: "X-User"}},
		{"invalid header", roleToken(t), ProxyAuth{Header: "X User", Trusted: trusted}},
		{"authorization header", roleToken(t), ProxyAuth{Header: "authorization", Trusted: trusted}},
	} {
		if _, err := tc.token.WithProxy(tc.proxy); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	for _, spec := range []string{"alice", "alice=owner", "alice=admin,ALICE=viewer"} {
		if _, err := ParseProxyRoles(spec); err == nil {
			t.Errorf("ParseProxyRoles(%q) accepted", spec)
		}
	}
	if _, err := ParsePrefixes("10.0.0.0/33"); err == nil {
		t.Error("invalid prefix accepted")
	}
}
