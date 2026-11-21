package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/demo"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInvestigateGroupExplainsTheDemoTimeouts(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "investigate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := demo.Seed(ctx, store, time.Now()); err != nil {
		t.Fatal(err)
	}
	groups, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, Search: "HttpTimeoutException"}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	group := groups.Items[0]

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := investigationServer(store, ownership.Owners{}).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var result investigateResult
	callTool(t, session, "investigate_group", map[string]any{"group_id": investigation.EncodeGroupKey(group.Key)}, &result)

	if result.Group.Count != group.Count || result.Trace.Trace.TraceID != group.TraceID || len(result.Trace.Spans) < 5 {
		t.Fatalf("group %+v, trace %+v", result.Group, result.Trace.Trace)
	}
	var stack string
	for _, span := range result.EvidenceSpans {
		for _, exception := range span.Exceptions {
			if value, ok := exception.Attributes["exception.stacktrace"].(string); ok {
				stack = value
			}
		}
	}
	if !strings.Contains(stack, "GatewayClient.authorize") {
		t.Fatalf("evidence spans carry no stack trace: %+v", result.EvidenceSpans)
	}
	encoded, _ := json.Marshal(result.EvidenceSpans)
	if strings.Contains(string(encoded), "url.full") || strings.Contains(string(encoded), "deployment.environment") {
		t.Fatalf("evidence spans expose attributes beyond code and exceptions: %s", encoded)
	}
	if result.Baseline == nil || result.Baseline.Evidence != nil || result.DeltaNS == nil {
		t.Fatalf("baseline = %+v, delta %v", result.Baseline, result.DeltaNS)
	}
	var entries []string
	for _, version := range result.Versions {
		entries = append(entries, version.EntryVersion)
	}
	if !slices.Equal(entries, []string{demo.Baseline, demo.Candidate}) || !strings.Contains(strings.Join(result.Next, " "), "compare_versions") {
		t.Fatalf("versions %+v, next %v", result.Versions, result.Next)
	}
	if outcome := callTool(t, session, "investigate_group", map[string]any{"group_id": "not-a-group"}, nil); !outcome.IsError {
		t.Fatal("an invalid group ID was accepted")
	}
}
