package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/config"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestSeparateListenersExportAndShutdown(t *testing.T) {
	t.Run("local", func(t *testing.T) { testSeparateListeners(t, "") })
	t.Run("token", func(t *testing.T) { testSeparateListeners(t, strings.Repeat("0123456789abcdef", 2)) })
}

func testSeparateListeners(t *testing.T, secret string) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg := config.Config{ListenAddr: "127.0.0.1:0", DashboardAddr: "127.0.0.1:0", GRPCAddr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "geko.db"), MaxDBMiB: 256}
	token, err := access.ParseToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccessToken = token
	ready := make(startupOutput, 3)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ready) }()
	var ingest, dashboard, rpc string
	for range 3 {
		select {
		case line := <-ready:
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Geko listening on "):
				ingest = strings.TrimPrefix(line, "Geko listening on ")
			case strings.HasPrefix(line, "Dashboard listening on "):
				dashboard = strings.TrimPrefix(line, "Dashboard listening on ")
			case strings.HasPrefix(line, "OTLP/gRPC listening on "):
				rpc = strings.TrimPrefix(line, "OTLP/gRPC listening on ")
			}
		case err := <-done:
			t.Fatalf("startup: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	requestHTTP := func(method, url, auth string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if secret != "" {
			req.Host = "geko.example.test"
			switch auth {
			case "basic":
				req.SetBasicAuth("geko", secret)
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+secret)
			case "wrong":
				req.Header.Set("Authorization", "Bearer wrong")
			}
		}
		if auth == "cross-origin" {
			req.SetBasicAuth("geko", secret)
			req.Header.Set("Origin", "https://other.example.test")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if token.Enabled() {
		for _, route := range []struct {
			method, url, auth string
			status            int
		}{
			{"GET", dashboard + "/settings", "", 401},
			{"GET", dashboard + "/events/failures", "wrong", 401},
			{"GET", ingest + "/healthz", "", 401},
			{"POST", ingest + "/v1/traces", "basic", 401},
			{"POST", ingest + "/v1/traces", "bearer", 415},
			{"POST", dashboard + "/traces/import", "cross-origin", 403},
		} {
			response := requestHTTP(route.method, route.url, route.auth)
			response.Body.Close()
			if response.StatusCode != route.status {
				t.Fatalf("%s %s = %d, want %d", route.method, route.url, response.StatusCode, route.status)
			}
		}
	}
	for _, base := range []string{ingest, dashboard} {
		response := requestHTTP("GET", base+"/healthz", "bearer")
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("health %s = %d", base, response.StatusCode)
		}
	}
	response := requestHTTP("GET", ingest+"/api/v1/failures", "bearer")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("ingest listener exposes query API or lacks CSP")
	}
	response = requestHTTP("POST", dashboard+"/v1/traces", "bearer")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("dashboard accepts ingest")
	}
	conn, err := grpc.NewClient(rpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{{TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "grpc-demo", StartTimeUnixNano: 1, EndTimeUnixNano: 2}}}}}}}
	rpcClient := collector.NewTraceServiceClient(conn)
	exportContext := ctx
	if token.Enabled() {
		if _, err := rpcClient.Export(ctx, request); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unauthenticated gRPC export: %v", err)
		}
		exportContext = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+secret)
	}
	if _, err := rpcClient.Export(exportContext, request); err != nil {
		t.Fatal(err)
	}
	response = requestHTTP("GET", dashboard+"/api/v1/traces/"+strings.Repeat("01", 16), "bearer")
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("grpc-demo")) {
		t.Fatalf("trace=%d %s %v", response.StatusCode, body, err)
	}
	response = requestHTTP("GET", dashboard+"/settings", "basic")
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !bytes.Contains(body, []byte(rpc)) || !bytes.Contains(body, []byte(strings.TrimPrefix(ingest, "http://"))) ||
		(secret != "" && bytes.Contains(body, []byte(secret))) {
		t.Fatalf("settings=%s, %v", body, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown timed out")
	}
	for _, addr := range []string{strings.TrimPrefix(ingest, "http://"), strings.TrimPrefix(dashboard, "http://"), rpc} {
		listener, err := listen(t.Context(), addr)
		if err != nil {
			t.Fatalf("listener not released: %v", err)
		}
		listener.Close()
	}
}

func TestOptionalListenerFailureAbortsStartup(t *testing.T) {
	busy, err := listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, kind := range []string{"dashboard", "grpc"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.Config{ListenAddr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "geko.db"), MaxDBMiB: 256}
			if kind == "dashboard" {
				cfg.DashboardAddr = busy.Addr().String()
			} else {
				cfg.GRPCAddr = busy.Addr().String()
			}
			var out bytes.Buffer
			if err := serve(t.Context(), cfg, &out); err == nil {
				t.Fatal("startup ignored occupied optional listener")
			}
			if out.Len() != 0 {
				t.Fatalf("reported ready on failed startup: %s", out.String())
			}
		})
	}
}
