package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/demo"
)

// listening reads startup output until the HTTP listener's URL.
func listening(t *testing.T, ctx context.Context, out startupOutput, done <-chan error) string {
	t.Helper()
	for {
		select {
		case line := <-out:
			if address, ok := strings.CutPrefix(strings.TrimSpace(line), "Geko listening on "); ok {
				return address
			}
		case err := <-done:
			t.Fatalf("exited before listening: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func fetch(t *testing.T, request *http.Request) (int, string) {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func TestDemoOpensTheSeededDashboardAndRefusesChangesWhenReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	opened := make(chan string, 1)
	previous := startBrowser
	startBrowser = func(url string) error { opened <- url; return nil }
	t.Cleanup(func() { startBrowser = previous })
	out := make(startupOutput, 32)
	done := make(chan error, 1)
	go func() { done <- demoCommand(ctx, []string{"--read-only"}, out) }()
	base := listening(t, ctx, out, done)
	if got := <-opened; got != base+"/" {
		t.Fatalf("opened %q, want %q", got, base+"/")
	}
	get := func(path string) (int, string) {
		request, _ := http.NewRequest(http.MethodGet, base+path, nil)
		return fetch(t, request)
	}
	if status, body := get("/failures?view=groups"); status != http.StatusOK || !strings.Contains(body, "HttpTimeoutException") ||
		!strings.Contains(body, "This instance is read-only") || strings.Contains(body, `action="/groups/state"`) {
		t.Fatalf("groups page = %d:\n%s", status, body)
	}
	if status, body := get("/regressions?" + url.Values{"baseline": {demo.Baseline}, "candidate": {demo.Candidate}}.Encode()); status != http.StatusOK || !strings.Contains(body, "Failure rate") {
		t.Fatalf("regressions page = %d:\n%s", status, body)
	}
	for _, path := range []string{"/v1/traces", "/groups/state", "/traces/import", "/applications"} {
		request, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		if status, _ := fetch(t, request); status != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403", path, status)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPublicServerAdmitsAnyHostButNoChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "public.db"),
		RetentionDays: config.DefaultRetentionDays, MaxDBMiB: config.DefaultMaxDBMiB, ReadOnly: true,
	}
	out := make(startupOutput, 8)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, out, serveOptions{public: true}) }()
	base := listening(t, ctx, out, done)
	request, _ := http.NewRequest(http.MethodGet, base+"/api/v1/failures", nil)
	request.Host = "demo.example.com"
	if status, body := fetch(t, request); status != http.StatusOK {
		t.Fatalf("public read from another Host = %d: %s", status, body)
	}
	request, _ = http.NewRequest(http.MethodPost, base+"/v1/traces", strings.NewReader("{}"))
	request.Host = "demo.example.com"
	request.Header.Set("Content-Type", "application/json")
	if status, _ := fetch(t, request); status != http.StatusForbidden {
		t.Fatalf("public export = %d, want 403", status)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyRefusesGRPC(t *testing.T) {
	cfg := config.Config{ListenAddr: "127.0.0.1:0", GRPCAddr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "grpc.db"), ReadOnly: true}
	if err := serve(t.Context(), cfg, io.Discard, serveOptions{}); err == nil || !strings.Contains(err.Error(), "grpc_addr") {
		t.Fatalf("serve = %v", err)
	}
}

func TestLimitPerClient(t *testing.T) {
	proxies, err := parsePrefixes("10.0.0.0/8, 192.0.2.9")
	if err != nil {
		t.Fatal(err)
	}
	handler := limitPerClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), 0.001, 2, proxies)
	status := func(client string, forwarded ...string) int {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = client
		for _, value := range forwarded {
			request.Header.Add("X-Forwarded-For", value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if got := status("192.0.2.1:5000"); got != want {
			t.Fatalf("request %d = %d, want %d", i+1, got, want)
		}
	}
	if got := status("192.0.2.2:5000"); got != http.StatusOK {
		t.Fatalf("another client = %d, want 200", got)
	}
	// Behind a trusted proxy, the address it appended is the client.
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if got := status("10.1.2.3:443", "203.0.113.50, 198.51.100.7"); got != want {
			t.Fatalf("proxied request %d = %d, want %d", i+1, got, want)
		}
	}
	if got := status("192.0.2.9:443", "198.51.100.8"); got != http.StatusOK {
		t.Fatalf("another proxied client = %d, want 200", got)
	}
	// An untrusted peer cannot choose its address.
	if got := status("192.0.2.1:6000", "198.51.100.9"); got != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For = %d, want 429", got)
	}
	if _, err := parsePrefixes("proxy.internal"); err == nil {
		t.Fatal("a host name is not a trusted proxy")
	}
}

func TestServeFlags(t *testing.T) {
	if err := run(t.Context(), []string{"serve", "extra"}, io.Discard); err == nil || !strings.Contains(err.Error(), "usage: geko serve") {
		t.Fatalf("serve extra = %v", err)
	}
	if err := run(t.Context(), []string{"demo", "--listen", "localhost:80"}, io.Discard); err == nil || !strings.Contains(err.Error(), "must be an IP address") {
		t.Fatalf("demo --listen localhost = %v", err)
	}
}
