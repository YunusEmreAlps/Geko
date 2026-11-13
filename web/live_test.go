package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestFailureEventsAndGroups(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	shutdown := make(chan struct{})
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Shutdown: shutdown}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/failures", nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatalf("headers=%v", response.Header)
	}
	reader := bufio.NewReader(response.Body)
	readEvent := func() string {
		t.Helper()
		var data strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\n" {
				return data.String()
			}
			data.WriteString(line)
		}
	}
	if event := readEvent(); !strings.Contains(event, "event: ready") {
		t.Fatalf("ready=%s", event)
	}
	span := telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, Name: "<script>unsafe</script>", ServiceName: "api", StatusCode: telemetry.StatusError, StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0)}
	if err := store.WriteSpans(t.Context(), time.Now(), []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	if event := readEvent(); !strings.Contains(event, "event: changed") || strings.Contains(event, "unsafe") {
		t.Fatalf("change=%s", event)
	}
	page, err := server.Client().Get(server.URL + "/failures?view=groups")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(page.Body)
	page.Body.Close()
	if err != nil || page.StatusCode != 200 || !strings.Contains(string(body), "Error groups") || !strings.Contains(string(body), "First received") || strings.Contains(string(body), "<script>unsafe</script>") {
		t.Fatalf("groups=%d %s, %v", page.StatusCode, body, err)
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 1}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("groups=%+v, %v", groups, err)
	}
	group := groups.Items[0]
	detail := httptest.NewRecorder()
	path := "http://127.0.0.1/failures?group=" + investigation.EncodeGroupKey(group.Key)
	mux.ServeHTTP(detail, httptest.NewRequest("GET", path, nil))
	detailBody := detail.Body.String()
	evidencePath := "/traces/" + group.TraceID + "?span=" + group.SpanID + "#span-details"
	if detail.Code != 200 || !strings.Contains(detailBody, "Observed failure summary") ||
		!strings.Contains(detailBody, "1 matching trace") || !strings.Contains(detailBody, evidencePath) ||
		strings.Contains(detailBody, "<script>unsafe</script>") {
		t.Fatalf("group summary=%d %s", detail.Code, detailBody)
	}
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest("GET", "http://rebind.example/events/failures", nil))
	if bad.Code != http.StatusForbidden {
		t.Fatalf("host=%d", bad.Code)
	}
	for _, path := range []string{"/failures?view=oops", "/failures?view=groups&group_cursor=invalid", "/failures?group=invalid"} {
		record := httptest.NewRecorder()
		mux.ServeHTTP(record, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		if record.Code != 400 {
			t.Fatalf("%s=%d", path, record.Code)
		}
	}
	close(shutdown)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
}

func TestFailureEventsRejectCrossSiteBrowsers(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{Shutdown: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	for site, want := range map[string]int{"cross-site": http.StatusForbidden, "same-site": http.StatusForbidden} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/events/failures", nil)
		request.Header.Set("Sec-Fetch-Site", site)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("Sec-Fetch-Site %s: status %d", site, response.Code)
		}
	}
}
