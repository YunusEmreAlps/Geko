package web

import (
	"bytes"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// A team note and issue link appear on the group, in its copied bug evidence
// and in the audit log, which leaves out the note's text.
func TestGroupNoteAndBugEvidence(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed := time.Now().UTC().Add(-time.Hour)
	if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{{
		TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError, Attributes: telemetry.Attributes{"http.response.status_code": 503},
		StartTime: observed, EndTime: observed.Add(42 * time.Millisecond),
	}}); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{
		Audit: audit.New(&log), PublicURL: "https://geko.example.com",
		TraceLinkURL: "https://grafana.example.com/explore?trace={trace_id}", TraceLinkName: "Tempo",
	}); err != nil {
		t.Fatal(err)
	}
	do := func(method, target string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+target, strings.NewReader(form.Encode()))
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 1}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatal(groups, err)
	}
	group := investigation.EncodeGroupKey(groups.Items[0].Key)
	summaryPath := "/failures?group=" + url.QueryEscape(group)

	if w := do(http.MethodPost, "/groups/note", url.Values{"group": {group}, "issue_url": {"javascript:alert(1)"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe issue link = %d", w.Code)
	}
	w := do(http.MethodPost, "/groups/note", url.Values{
		"group": {group}, "note": {"Card 4111 seen in logs\r\nsee runbook"}, "issue_url": {"https://jira.example.com/browse/PAY-12"}, "return": {summaryPath},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != summaryPath+"#group-note" {
		t.Fatalf("save note = %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	page := html.UnescapeString(do(http.MethodGet, summaryPath, nil).Body.String())
	for _, want := range []string{
		`<p class="note-text">Card 4111 seen in logs` + "\nsee runbook</p>",
		`>jira.example.com/browse/PAY-12</a>`,
		"**HTTP 5xx on `POST /pay` in checkout**\n",
		"- Issue: https://jira.example.com/browse/PAY-12\n",
		"- Group: https://geko.example.com/failures?group=" + url.QueryEscape(group) + "\n",
		"- Latest example: https://geko.example.com/traces/01000000000000000000000000000000?span=0100000000000000\n",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("group summary lacks %q:\n%s", want, page)
		}
	}
	if list := do(http.MethodGet, "/failures?view=groups", nil).Body.String(); !strings.Contains(list, `href="https://jira.example.com/browse/PAY-12" target="_blank" rel="noopener noreferrer" title="https://jira.example.com/browse/PAY-12">Issue</a>`) {
		t.Fatal("group list lacks the issue link")
	}
	if !strings.Contains(log.String(), `"action":"group.note"`) || !strings.Contains(log.String(), `"note":"saved"`) || strings.Contains(log.String(), "4111") {
		t.Fatalf("audit log = %s", log.String())
	}

	trace := html.UnescapeString(do(http.MethodGet, "/traces/01000000000000000000000000000000", nil).Body.String())
	for _, want := range []string{
		`href="https://grafana.example.com/explore?trace=01000000000000000000000000000000"`,
		">Open in Tempo</a>",
		"**HTTP 5xx on `POST /pay` in checkout**\n- Request: `POST /pay` in checkout, HTTP 503, 42ms\n",
		"- Trace: https://geko.example.com/traces/01000000000000000000000000000000?span=0100000000000000\n",
	} {
		if !strings.Contains(trace, want) {
			t.Fatalf("trace page lacks %q:\n%s", want, trace)
		}
	}

	if w := do(http.MethodPost, "/groups/note", url.Values{"group": {group}}); w.Code != http.StatusSeeOther {
		t.Fatalf("clear note = %d", w.Code)
	}
	if page := do(http.MethodGet, summaryPath, nil).Body.String(); strings.Contains(page, "note-text") || !strings.Contains(page, "Add a note or issue link") {
		t.Fatal("cleared note still shown")
	}
}

func TestTraceBugEvidenceWithException(t *testing.T) {
	status, duration := 500, int64(490*time.Millisecond)
	got := traceBugEvidence("http://127.0.0.1:4318", investigation.TraceSummary{
		TraceID: "ab", LastObserved: time.Date(2026, 11, 14, 8, 10, 12, 0, time.UTC),
		EntrySpan: &investigation.SpanSummary{Service: "frontend", Operation: "POST /checkout", HTTPStatus: &status, DurationNS: &duration},
		Evidence: &investigation.Evidence{
			SpanSummary:    investigation.SpanSummary{SpanID: "cd", Service: "payments", Application: "shop", Operation: "pay`ment"},
			Classification: "exception", ExceptionType: "*errors.errorString", ExceptionMessage: "gateway\n  timed out",
		},
	})
	want := "**Exception on `pay'ment` in shop / payments**\n" +
		"- Request: `POST /checkout` in frontend, HTTP 500, 490ms\n" +
		"- Exception: `*errors.errorString`: gateway timed out\n" +
		"- Received: 14 Nov 2026, 08:10:12 UTC\n" +
		"- Trace: http://127.0.0.1:4318/traces/ab?span=cd\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if traceBugEvidence("", investigation.TraceSummary{}) != "" {
		t.Fatal("a trace without evidence produced bug evidence")
	}
}
