package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExistingMCPSessionUsesLiveOwners(t *testing.T) {
	dir := t.TempDir()
	data := []byte("# live owners\n")
	path := filepath.Join(dir, "geko.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := config.NewRuntime(config.Config{Source: config.Source{FilePath: path, FileRevision: fmt.Sprintf("%x", sha256.Sum256(data))}})
	store, err := sqlite.Open(t.Context(), filepath.Join(dir, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, ServiceName: "api", Name: "GET /", Kind: telemetry.KindServer, StatusCode: telemetry.StatusError, StartTime: now, EndTime: now.Add(time.Millisecond)}}); err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := investigationServer(store, ownership.Dynamic(r.Owners)).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "owner-test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, team := range []string{"first-team", "second-team"} {
		if _, err := r.UpdateOwners(t.Context(), r.Snapshot().Revision, config.OwnerMappings{Services: map[string]string{"api": team}}); err != nil {
			t.Fatal(err)
		}
		var page queryPage[failureGroupCitation]
		callTool(t, client, "list_failure_groups", map[string]any{}, &page)
		if len(page.Items) != 1 || page.Items[0].Owner != team {
			t.Fatalf("existing MCP session has stale owner: %+v", page)
		}
	}
}
