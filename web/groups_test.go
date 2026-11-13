package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestGroupTriage(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "triage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	record := func(trace byte, observed time.Time) {
		t.Helper()
		if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{{
			TraceID: telemetry.TraceID{trace}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
			ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError,
			StartTime: observed, EndTime: observed.Add(time.Millisecond),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	record(1, time.Now().UTC().Add(-time.Hour))
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if w.Code != status {
			t.Fatalf("%s returned %d: %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	post := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/groups/state", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	groups := get("/failures?view=groups", http.StatusOK)
	match := regexp.MustCompile(`href="/failures\?group=([^"&]+)"`).FindStringSubmatch(groups)
	if match == nil || !strings.Contains(groups, `aria-label="Group state"`) || !strings.Contains(groups, "data-first-received=") {
		t.Fatalf("group list lacks a group link or state filter: %s", groups)
	}
	group, err := url.QueryUnescape(html.UnescapeString(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	summary := get("/failures?group="+url.QueryEscape(group), http.StatusOK)
	if !strings.Contains(summary, `value="resolved"`) || !strings.Contains(summary, `value="muted"`) {
		t.Fatal("group summary lacks resolve and mute actions")
	}

	for _, form := range []url.Values{
		{"group": {"invalid"}, "state": {"resolved"}},
		{"group": {group}, "state": {"reopened"}},
	} {
		if w := post(form); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid change %v returned %d", form, w.Code)
		}
	}
	w := post(url.Values{"group": {group}, "state": {"resolved"}, "return": {"https://evil.example/"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/failures?view=groups" {
		t.Fatalf("resolve = %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if body := get("/failures?view=groups", http.StatusOK); strings.Contains(body, "POST /pay") {
		t.Fatal("resolved group still listed as open")
	}
	if body := get("/failures?view=groups&state=resolved", http.StatusOK); !strings.Contains(body, "POST /pay") || !strings.Contains(body, ">Resolved</span>") {
		t.Fatal("resolved group missing from the resolved list")
	}
	if body := get("/failures?group="+url.QueryEscape(group), http.StatusOK); !strings.Contains(body, ">Reopen</button>") {
		t.Fatal("resolved group summary lacks a reopen action")
	}
	get("/failures?state=resolved", http.StatusBadRequest)
	get("/failures?view=groups&state=closed", http.StatusBadRequest)

	record(2, time.Now().UTC().Add(time.Second))
	if body := get("/failures?view=groups", http.StatusOK); !strings.Contains(body, ">Reopened</span>") {
		t.Fatal("recurring group not shown as reopened")
	}

	w = post(url.Values{"group": {group}, "state": {"muted"}, "return": {"/failures?view=groups&state=muted"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/failures?view=groups&state=muted" {
		t.Fatalf("mute = %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if body := get("/failures?view=groups&state=muted", http.StatusOK); !strings.Contains(body, ">Muted</span>") {
		t.Fatal("muted group missing from the muted list")
	}
}

func TestResolveInVersionForm(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "version.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{{
		TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError, StartTime: now, EndTime: now,
	}}); err != nil {
		t.Fatal(err)
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 1}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatal(groups, err)
	}
	group := investigation.EncodeGroupKey(groups.Items[0].Key)
	form := url.Values{"group": {group}, "state": {"resolved"}, "version": {"2.4.0"}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/groups/state", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("resolve = %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/failures?view=groups&state=resolved", nil))
	if !strings.Contains(w.Body.String(), ">Resolved in 2.4.0</span>") {
		t.Fatal("resolved version missing from the group list")
	}
}
