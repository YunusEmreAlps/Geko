package web

import (
	"bytes"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// A triager resolves a group from the dashboard; the group and the audit log
// name them, and admin-only actions stay hidden.
func TestTriagerSession(t *testing.T) {
	const admin, triager = "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8", "c6a1f0d9b7e4c2a83f9c1e7a0b5d48e2"
	token, err := access.ParseToken(admin)
	if err == nil {
		token, err = token.WithTriageTokens("qa=" + triager)
	}
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed := time.Now().UTC().Add(-time.Hour)
	if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{{
		TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError,
		StartTime: observed, EndTime: observed.Add(time.Millisecond),
	}}); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{
		AccessToken: true, SignIn: token.SignIn, SignOut: token.SignOut, Tokens: token, Audit: audit.New(&log),
	}); err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(mux)
	do := func(method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://geko.example.com"+target, strings.NewReader(form.Encode()))
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		r.Header.Set("Accept", "text/html")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	w := do(http.MethodPost, "/login", url.Values{"token": {triager}, "next": {"/failures?view=groups"}}, nil)
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "geko_session" {
			session = cookie
		}
	}
	if w.Code != http.StatusSeeOther || session == nil {
		t.Fatalf("triage sign-in = %d", w.Code)
	}
	groups := do(http.MethodGet, "/failures?view=groups", nil, session).Body.String()
	if !strings.Contains(groups, `<span class="nav-identity-name">qa</span> <span class="subtle">triager</span>`) {
		t.Fatalf("sidebar does not name the triager: %s", groups)
	}
	match := regexp.MustCompile(`href="/failures\?group=([^"&]+)"`).FindStringSubmatch(groups)
	if match == nil {
		t.Fatal("no group link")
	}
	group, err := url.QueryUnescape(html.UnescapeString(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if summary := do(http.MethodGet, "/failures?group="+url.QueryEscape(group), nil, session).Body.String(); !strings.Contains(summary, `value="resolved"`) {
		t.Fatal("triager does not see the resolve action")
	}
	if page := do(http.MethodGet, "/applications", nil, session).Body.String(); strings.Contains(page, `action="/applications"`) {
		t.Fatal("triager sees application changes only admins can make")
	}
	if w := do(http.MethodPost, "/applications", url.Values{"action": {"mapping"}, "service": {"a"}, "application": {"b"}}, session); w.Code != http.StatusForbidden {
		t.Fatalf("triager changed applications: %d", w.Code)
	}
	if w := do(http.MethodPost, "/groups/state", url.Values{"group": {group}, "state": {"resolved"}, "version": {"2.1"}}, session); w.Code != http.StatusSeeOther {
		t.Fatalf("triager resolve = %d: %s", w.Code, w.Body)
	}
	if body := do(http.MethodGet, "/failures?view=groups&state=resolved", nil, session).Body.String(); !strings.Contains(body, " by qa;") {
		t.Fatalf("resolved group does not name who resolved it: %s", body)
	}
	do(http.MethodPost, "/logout", url.Values{}, session)
	do(http.MethodPost, "/login", url.Values{"token": {"wrong-token-wrong-token-wrong-tok"}}, nil)

	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	for i, want := range []string{
		`"action":"sign_in","actor":"qa","role":"triager","via":"session"`,
		`"action":"group.state","actor":"qa","role":"triager","via":"session"`,
		`"action":"sign_out","actor":"qa","role":"triager","via":"session"`,
		`"action":"sign_in","actor":"unknown"`,
	} {
		if i >= len(lines) || !strings.Contains(lines[i], want) {
			t.Fatalf("audit line %d lacks %s:\n%s", i, want, log.String())
		}
	}
	if !strings.Contains(lines[1], `"detail":{"state":"resolved","version":"2.1"}`) || !strings.Contains(lines[3], `"result":"denied"`) ||
		strings.Contains(log.String(), triager) {
		t.Fatalf("audit log = %s", log.String())
	}
}
