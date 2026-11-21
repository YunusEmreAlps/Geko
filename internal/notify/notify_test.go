package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type received struct {
	header http.Header
	body   string
}

func webhook(t *testing.T, status int) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var messages []received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		messages = append(messages, received{r.Header.Clone(), string(body)})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, func() []received {
		mu.Lock()
		defer mu.Unlock()
		result := messages
		messages = nil
		return result
	}
}

func record(t *testing.T, store *sqlite.Store, trace byte, operation string, at time.Time) {
	t.Helper()
	if err := store.WriteSpans(t.Context(), at, []telemetry.Span{{
		TraceID: telemetry.TraceID{trace}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "checkout", Name: operation, StatusCode: telemetry.StatusError,
		StartTime: at, EndTime: at.Add(time.Millisecond),
		Attributes: telemetry.Attributes{"http.response.status_code": 503},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestNewAndReopenedGroups(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, messages := webhook(t, http.StatusOK)
	start := time.Now().UTC().Truncate(time.Second)
	// A group received well before the notifier started is not new.
	record(t, store, 9, "GET /old", start.Add(-time.Hour))
	n := New(server.URL, JSON, "https://geko.example.com/", store, start)

	record(t, store, 1, "POST /pay", start.Add(time.Second))
	if err := n.Check(ctx, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	got := messages()
	if len(got) != 1 {
		t.Fatalf("messages = %+v", got)
	}
	var payload struct {
		Event string `json:"event"`
		Text  string `json:"text"`
		URL   string `json:"url"`
		Group struct {
			Operation string `json:"evidence_operation"`
			Count     int    `json:"trace_count"`
		} `json:"group"`
	}
	if err := json.Unmarshal([]byte(got[0].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != "new_group" || payload.Group.Operation != "POST /pay" || payload.Group.Count != 1 ||
		!strings.HasPrefix(payload.URL, "https://geko.example.com/failures?group=") || !strings.Contains(payload.Text, "evidence HTTP 503") {
		t.Fatalf("payload = %+v", payload)
	}
	// The overlap window rechecks the same group without repeating it.
	if err := n.Check(ctx, start.Add(3*time.Second)); err != nil || len(messages()) != 0 {
		t.Fatalf("repeated notification, err %v", err)
	}

	groups, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: 10, Search: "POST /pay"}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	key := groups.Items[0].Key
	if err := store.SetGroupState(ctx, key, investigation.GroupResolved, "", "", start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	record(t, store, 2, "POST /pay", start.Add(5*time.Second))
	if err := n.Check(ctx, start.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := messages(); len(got) != 1 || !strings.Contains(got[0].body, `"event":"reopened_group"`) {
		t.Fatalf("reopened messages = %+v", got)
	}
	record(t, store, 3, "POST /pay", start.Add(7*time.Second))
	if err := n.Check(ctx, start.Add(8*time.Second)); err != nil || len(messages()) != 0 {
		t.Fatalf("reopened group notified twice, err %v", err)
	}

	if err := store.SetGroupState(ctx, key, investigation.GroupMuted, "", "", start.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	record(t, store, 4, "POST /pay", start.Add(10*time.Second))
	if err := n.Check(ctx, start.Add(11*time.Second)); err != nil || len(messages()) != 0 {
		t.Fatalf("muted group notified, err %v", err)
	}

	for i := range 7 {
		record(t, store, byte(20+i), "GET /burst/"+string(rune('a'+i)), start.Add(12*time.Second))
	}
	if err := n.Check(ctx, start.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	got = messages()
	if len(got) != maxMessages+1 || !strings.Contains(got[maxMessages].body, `"count":2`) {
		t.Fatalf("burst messages = %d: %+v", len(got), got)
	}
	if err := n.Check(ctx, start.Add(14*time.Second)); err != nil || len(messages()) != 0 {
		t.Fatalf("summarized groups notified again, err %v", err)
	}
}

func TestApplicationWebhooks(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Now().UTC().Truncate(time.Second)
	recordIn := func(trace byte, application, operation string, at time.Time) {
		t.Helper()
		if err := store.WriteSpans(ctx, at, []telemetry.Span{{
			TraceID: telemetry.TraceID{trace}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
			ServiceName: "api", Name: operation, StatusCode: telemetry.StatusError,
			StartTime: at, EndTime: at.Add(time.Millisecond),
			ResourceAttributes: telemetry.Attributes{"service.namespace": application},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	shared, sharedMessages := webhook(t, http.StatusOK)
	storefront, storefrontMessages := webhook(t, http.StatusOK)
	routes := map[string]Webhook{"Storefront": {storefront.URL, JSON}}

	n := New(shared.URL, JSON, "https://geko.example.com", store, start).WithApplications(routes)
	recordIn(1, "Storefront", "POST /pay", start.Add(time.Second))
	recordIn(2, "Reporting", "GET /report", start.Add(time.Second))
	if err := n.Check(ctx, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := storefrontMessages(); len(got) != 1 || !strings.Contains(got[0].body, "POST /pay") {
		t.Fatalf("storefront messages = %+v", got)
	}
	if got := sharedMessages(); len(got) != 1 || !strings.Contains(got[0].body, "GET /report") {
		t.Fatalf("shared messages = %+v", got)
	}

	// Without a shared webhook, other applications' groups send nothing.
	n = New("", JSON, "https://geko.example.com", store, start.Add(3*time.Second)).WithApplications(routes)
	recordIn(3, "Reporting", "GET /export", start.Add(4*time.Second))
	for i := range 7 {
		recordIn(byte(20+i), "Storefront", "GET /burst/"+string(rune('a'+i)), start.Add(4*time.Second))
	}
	if err := n.Check(ctx, start.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := sharedMessages(); len(got) != 0 {
		t.Fatalf("shared webhook used without a URL: %+v", got)
	}
	// The new notifier's overlap window also covers POST /pay: 8 new groups.
	got := storefrontMessages()
	if len(got) != maxMessages+1 || !strings.Contains(got[maxMessages].body, `"count":3`) ||
		!strings.Contains(got[maxMessages].body, "application=group%3AStorefront") {
		t.Fatalf("storefront burst = %d: %+v", len(got), got)
	}

	if err := n.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if got := storefrontMessages(); len(got) != 1 || !strings.Contains(got[0].body, "working for Storefront") {
		t.Fatalf("test messages = %+v", got)
	}
}

func TestFormats(t *testing.T) {
	group := investigation.FailureGroup{
		Application: "Storefront", Service: "checkout", Operation: "<!channel> POST /pay\nnext",
		Classification: "http_5xx", Count: 2, HTTPStatus: 503,
	}
	for _, tc := range []struct {
		format Format
		check  func(t *testing.T, got received)
	}{
		{Slack, func(t *testing.T, got received) {
			if strings.Contains(got.body, "<!channel>") || !strings.Contains(got.body, "&lt;!channel&gt;") || !strings.Contains(got.body, "|Open in Geko>") {
				t.Fatalf("slack body %s", got.body)
			}
		}},
		{Discord, func(t *testing.T, got received) {
			if !strings.Contains(got.body, `"allowed_mentions":{"parse":[]}`) || !strings.Contains(got.body, "Storefront / checkout") {
				t.Fatalf("discord body %s", got.body)
			}
		}},
		{Ntfy, func(t *testing.T, got received) {
			if got.header.Get("Title") != "New error group" || !strings.HasPrefix(got.header.Get("Click"), "https://geko.example.com/") ||
				!strings.Contains(got.body, "HTTP 5xx in <!channel> POST /pay next") {
				t.Fatalf("ntfy request %v %s", got.header, got.body)
			}
		}},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			server, messages := webhook(t, http.StatusOK)
			n := New(server.URL, tc.format, "https://geko.example.com", nil, time.Now())
			if err := n.send(t.Context(), n.webhook, event{Kind: "new_group", Group: group, URL: n.groupURL("[]")}); err != nil {
				t.Fatal(err)
			}
			tc.check(t, messages()[0])
		})
	}
}

func TestTestMessageAndFailure(t *testing.T) {
	server, messages := webhook(t, http.StatusOK)
	if err := New(server.URL, JSON, "", nil, time.Now()).Test(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := messages(); len(got) != 1 || !strings.Contains(got[0].body, `"event":"test"`) || strings.Contains(got[0].body, `"group"`) {
		t.Fatalf("test message = %+v", got)
	}
	failing, _ := webhook(t, http.StatusInternalServerError)
	err := New(failing.URL+"/secret-path", JSON, "", nil, time.Now()).Test(t.Context())
	if err == nil || strings.Contains(err.Error(), "secret-path") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("failure = %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	err = New(closed.URL+"/secret-path", JSON, "", nil, time.Now()).Test(t.Context())
	if err == nil || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("connection failure = %v", err)
	}
}

func TestParseFormat(t *testing.T) {
	for _, tc := range []struct{ format, url string }{
		{"slack", "https://hooks.slack.com/services/x"},
		{"discord", "https://discord.com/api/webhooks/1/x"},
		{"ntfy", "https://ntfy.sh/topic"},
		{"json", "https://example.com/hook"},
		{"json", "https://discord.com/other"},
	} {
		if got, err := ParseFormat("", tc.url); err != nil || string(got) != tc.format {
			t.Errorf("ParseFormat(%q) = %q, %v", tc.url, got, err)
		}
	}
	if got, err := ParseFormat("ntfy", "https://ntfy.example.com/t"); err != nil || got != Ntfy {
		t.Errorf("explicit format = %q, %v", got, err)
	}
	if _, err := ParseFormat("teams", "https://example.com"); err == nil {
		t.Error("accepted an unknown format")
	}
}

// fullCounter counts the checks that grouped every stored failure.
type fullCounter struct {
	*sqlite.Store
	full int
}

func (c *fullCounter) FailureGroups(ctx context.Context, f investigation.FailureFilter, after *investigation.GroupCursor) (investigation.FailureGroups, error) {
	if f.From == nil {
		c.full++
	}
	return c.Store.FailureGroups(ctx, f, after)
}

func TestChecksGroupEveryFailureOnlyWhenAGroupIsUnsettled(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "settled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, messages := webhook(t, http.StatusOK)
	start := time.Now().UTC().Truncate(time.Second)
	record(t, store, 9, "GET /old", start.Add(-time.Hour))
	counter := &fullCounter{Store: store}
	n := New(server.URL, JSON, "https://geko.example.com/", counter, start)

	record(t, store, 1, "POST /pay", start.Add(time.Second))
	if err := n.Check(ctx, start.Add(2*time.Second)); err != nil || counter.full != 1 || len(messages()) != 1 {
		t.Fatalf("first check: %d full, err %v", counter.full, err)
	}
	// More failures of a settled group raise no event and need no full check.
	record(t, store, 2, "POST /pay", start.Add(3*time.Second))
	if err := n.Check(ctx, start.Add(4*time.Second)); err != nil || counter.full != 1 || len(messages()) != 0 {
		t.Fatalf("settled check: %d full, err %v", counter.full, err)
	}
	// An old group that recurs is checked in full, and is still not new.
	record(t, store, 10, "GET /old", start.Add(5*time.Second))
	if err := n.Check(ctx, start.Add(6*time.Second)); err != nil || counter.full != 2 || len(messages()) != 0 {
		t.Fatalf("recurring old group: %d full, err %v", counter.full, err)
	}
	record(t, store, 3, "GET /new", start.Add(7*time.Second))
	if err := n.Check(ctx, start.Add(8*time.Second)); err != nil || counter.full != 3 || len(messages()) != 1 {
		t.Fatalf("new group: %d full, err %v", counter.full, err)
	}
}

// notifyBenchStore holds 20,000 failing traces of one group, all observed an
// hour before the notifier starts.
func notifyBenchStore(b *testing.B, start time.Time) *sqlite.Store {
	b.Helper()
	store, err := sqlite.Open(b.Context(), filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	for batch := range 40 {
		spans := make([]telemetry.Span, 0, 500)
		for i := range 500 {
			id := telemetry.TraceID{byte(batch), byte(i >> 8), byte(i)}
			spans = append(spans, telemetry.Span{TraceID: id, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
				ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError,
				StartTime: start, EndTime: start.Add(time.Millisecond)})
		}
		if err := store.WriteSpans(b.Context(), start.Add(-time.Hour), spans); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

// BenchmarkCheck measures a check in steady state, against grouping every
// stored failure as each check did before.
func BenchmarkCheck(b *testing.B) {
	for _, full := range []bool{false, true} {
		b.Run(map[bool]string{false: "settled", true: "every-failure"}[full], func(b *testing.B) {
			start := time.Now().UTC()
			store := notifyBenchStore(b, start)
			n := New("http://127.0.0.1:9/", JSON, "http://127.0.0.1", store, start)
			for b.Loop() {
				start = start.Add(time.Second)
				if full {
					if _, err := n.groups(b.Context(), nil, start.Add(-time.Minute)); err != nil {
						b.Fatal(err)
					}
				} else if err := n.Check(b.Context(), start); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
