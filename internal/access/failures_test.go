package access

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailureLog(t *testing.T) {
	var lines []string
	original := logFailure
	logFailure = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { logFailure = original })
	now := time.Unix(1_700_000_000, 0)
	failures := newFailureLog()
	failures.now = func() time.Time { return now }
	request := func(address string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://geko.example/settings", nil)
		r.RemoteAddr = address + ":5000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9\nforged line")
		return r
	}
	for range 5 {
		failures.record(request("198.51.100.1"))
	}
	failures.record(request("198.51.100.2"))
	if len(lines) != 2 || !strings.Contains(lines[0], `"198.51.100.1"`) || strings.Contains(lines[0], "\nforged") {
		t.Fatalf("lines = %q", lines)
	}
	now = now.Add(time.Minute)
	failures.record(request("198.51.100.1"))
	if len(lines) != 4 || !strings.Contains(lines[2], "5 failed authentication attempts") {
		t.Fatalf("lines after a minute = %q", lines)
	}

	lines = nil
	failures = newFailureLog()
	failures.now = func() time.Time { return now }
	for i := range maxFailureSource + 10 {
		failures.record(request(fmt.Sprintf("10.0.%d.%d", i/256, i%256)))
	}
	if len(failures.sources) != maxFailureSource || len(lines) != maxFailureSource {
		t.Fatalf("tracked %d sources, logged %d lines", len(failures.sources), len(lines))
	}
}

func TestProtectLogsOnlyRejectedCredentials(t *testing.T) {
	var lines []string
	original := logFailure
	logFailure = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { logFailure = original })
	token, err := ParseToken(strings.Repeat("0123456789abcdef", 2))
	if err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://geko.example/settings", nil))
	if len(lines) != 0 {
		t.Fatalf("logged a sign-in challenge: %q", lines)
	}
	r := httptest.NewRequest(http.MethodGet, "http://geko.example/settings", nil)
	r.SetBasicAuth("geko", "wrong")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
}
