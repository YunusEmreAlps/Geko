package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"geko/internal/config"
)

func TestVersion(t *testing.T) {
	t.Setenv("GEKO_LISTEN_ADDR", "invalid")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Geko "+version) || !strings.Contains(out.String(), "go1.") {
		t.Fatalf("unexpected version output %q", out.String())
	}
}

func TestDoctorReportsBusyListener(t *testing.T) {
	listener, err := listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("GEKO_LISTEN_ADDR", listener.Addr().String())
	t.Setenv("GEKO_DB_PATH", filepath.Join(t.TempDir(), "geko.db"))

	var out bytes.Buffer
	err = run(t.Context(), []string{"doctor"}, &out)
	if err == nil || !strings.Contains(err.Error(), "GEKO_LISTEN_ADDR") {
		t.Fatalf("doctor error = %v, want a listener configuration hint", err)
	}
	if !strings.Contains(out.String(), "schema 1") {
		t.Fatalf("doctor output %q does not include the database result", out.String())
	}
}

func TestServeHealthAndShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cfg := config.Config{
		ListenAddr: "127.0.0.1:0",
		DBPath:     filepath.Join(t.TempDir(), "geko.db"),
	}
	ready := make(startupOutput, 1)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ready) }()

	var baseURL string
	select {
	case line := <-ready:
		baseURL = strings.TrimSpace(strings.TrimPrefix(line, "Geko listening on "))
	case err := <-done:
		t.Fatalf("serve exited before startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("health HTTP status = %d, want 204", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("server did not shut down")
	}

	listener, err := net.Listen("tcp", strings.TrimPrefix(baseURL, "http://"))
	if err != nil {
		t.Fatalf("listener not released: %v", err)
	}
	listener.Close()
}

type startupOutput chan string

func (out startupOutput) Write(p []byte) (int, error) {
	out <- string(p)
	return len(p), nil
}
