package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/demo"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInvestigateIncludesLatePrimaryEvidence(t *testing.T) {
	for _, earlierErrors := range []int{0, 12} {
		t.Run(fmt.Sprintf("earlier_errors_%d", earlierErrors), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "late.db")
			store, err := sqlite.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			start := time.Now().Add(-time.Minute)
			spans := make([]telemetry.Span, 120)
			for i := range spans {
				spans[i] = telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{byte(i + 1)},
					ServiceName: "api", Name: "work", StartTime: start.Add(time.Duration(i) * time.Millisecond), EndTime: start.Add(time.Second)}
				if i > 0 {
					spans[i].ParentID = spans[0].ID
				}
				if i < earlierErrors {
					spans[i].StatusCode = telemetry.StatusError
				}
			}
			spans[0].Kind = telemetry.KindServer
			primary := &spans[119]
			primary.Events = []telemetry.Event{{Name: "exception", Time: primary.StartTime, Attributes: telemetry.Attributes{
				"exception.type": "LateException", "exception.stacktrace": "Worker.finish:42", "private.detail": "hidden",
			}}}
			primary.Attributes = telemetry.Attributes{"code.function.name": "finish", "url.full": "hidden"}
			if err := store.WriteSpans(ctx, time.Now(), spans); err != nil {
				t.Fatal(err)
			}
			groups, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: 10}, nil)
			if err != nil || len(groups.Items) != 1 {
				t.Fatalf("groups = %+v, %v", groups, err)
			}
			reader, err := sqlite.OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			server, err := investigationServer(reader, ownership.Owners{}).Connect(ctx, serverTransport, nil)
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
			callTool(t, session, "investigate_group", map[string]any{"group_id": investigation.EncodeGroupKey(groups.Items[0].Key)}, &result)
			if len(result.Trace.Spans) != 100 || result.Trace.NextCursor == "" {
				t.Fatalf("timeline is not paginated: %+v", result.Trace)
			}
			want := hex.EncodeToString(primary.ID[:])
			if len(result.EvidenceSpans) != min(earlierErrors+1, maxEvidenceSpans) || result.EvidenceSpans[0].SpanID != want || result.EvidenceHasMore != (earlierErrors >= maxEvidenceSpans) {
				t.Fatalf("primary %s, evidence %+v, more %v", want, result.EvidenceSpans, result.EvidenceHasMore)
			}
			encoded, _ := json.Marshal(result.EvidenceSpans)
			if !strings.Contains(string(encoded), "Worker.finish:42") || strings.Contains(string(encoded), "hidden") {
				t.Fatalf("evidence filtering: %s", encoded)
			}
		})
	}
}

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
	if !slices.Equal(entries, []string{demo.Baseline, demo.Candidate}) || result.VersionCount != 2 || result.VersionsTruncated ||
		!strings.Contains(strings.Join(result.Next, " "), "compare_versions") || strings.Contains(strings.Join(result.Next, " "), "omitted") {
		t.Fatalf("versions %+v, next %v", result.Versions, result.Next)
	}
	if outcome := callTool(t, session, "investigate_group", map[string]any{"group_id": "not-a-group"}, nil); !outcome.IsError {
		t.Fatal("an invalid group ID was accepted")
	}
}

func TestNextStepsDiscloseTruncatedVersions(t *testing.T) {
	result := investigateResult{Versions: []investigation.GroupVersion{{EntryVersion: "1"}, {EntryVersion: "30"}}, VersionCount: 30, VersionsTruncated: true}
	steps := strings.Join(nextSteps(result), " ")
	if !strings.Contains(steps, "2 of 30 version pairs") || !strings.Contains(steps, "compare_versions") {
		t.Fatalf("steps = %s", steps)
	}
}
