package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadOnlyAgentQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geko.db")
	t.Setenv("GEKO_DB_PATH", path)
	t.Setenv("GEKO_CONFIG", "")
	t.Chdir(t.TempDir())
	ctx := t.Context()
	writer, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	now := time.Now()
	for _, id := range []byte{1, 2} {
		span := telemetry.Span{
			TraceID: telemetry.TraceID{id}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
			ServiceName: "orders", Name: "GET /orders", StartTime: now, EndTime: now.Add(time.Second),
			Attributes: telemetry.Attributes{"http.response.status_code": int64(503), "private_fixture": "hidden"},
		}
		if err := writer.WriteSpans(ctx, now, []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"failures", "--json", "--service", "orders", "--limit", "1"}, &output); err != nil {
		t.Fatal(err)
	}
	var page queryPage[investigation.TraceSummary]
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("CLI response = %s, %v", output.Bytes(), err)
	}
	reader, err := sqlite.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := investigationServer(reader).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools = %v, %v", tools, err)
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_failures", Arguments: map[string]any{"service": "orders", "limit": 1, "cursor": page.NextCursor},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP failures = %v, %v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	var next queryPage[investigation.TraceSummary]
	if err != nil || json.Unmarshal(data, &next) != nil || len(next.Items) != 1 || next.Items[0].TraceID == page.Items[0].TraceID {
		t.Fatalf("MCP next page = %s, %v", data, err)
	}
	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_trace", Arguments: map[string]any{"trace_id": next.Items[0].TraceID},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP trace = %v, %v", result, err)
	}
	data, err = json.Marshal(result.StructuredContent)
	if err != nil || !bytes.Contains(data, []byte("GET /orders")) || strings.Contains(string(data), "private_fixture") {
		t.Fatalf("MCP trace = %s, %v", data, err)
	}
	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_failure_groups", Arguments: map[string]any{"service": "orders", "status": 503},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP groups = %v, %v", result, err)
	}
	data, err = json.Marshal(result.StructuredContent)
	var groups queryPage[failureGroupCitation]
	if err != nil || json.Unmarshal(data, &groups) != nil || len(groups.Items) != 1 {
		t.Fatalf("MCP groups = %s, %v", data, err)
	}
	group := groups.Items[0]
	wantPath := "/traces/" + page.Items[0].TraceID + "?span=" + page.Items[0].Evidence.SpanID + "#span-details"
	if group.Count != 2 || group.Outcomes.HTTP5xx != 2 || group.EvidencePath != wantPath {
		t.Fatalf("MCP group = %+v, want evidence %s", group, wantPath)
	}
}

func TestFailuresCommandErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	t.Setenv("GEKO_DB_PATH", path)
	t.Setenv("GEKO_CONFIG", "")
	t.Chdir(t.TempDir())
	var output bytes.Buffer
	for args, want := range map[string]string{
		"--limit 0":   "--limit must be between 1 and 100",
		"--limit 101": "--limit must be between 1 and 100",
		"":            "no Geko database at " + path,
	} {
		argv := []string{"failures", "--json"}
		if args != "" {
			argv = append(argv, strings.Fields(args)...)
		}
		if err := run(t.Context(), argv, &output); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", args, err, want)
		}
	}
}
