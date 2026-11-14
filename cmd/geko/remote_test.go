package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	remoteAdmin = "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8"
	remoteRead  = "b7e4c2a83f9c1e7a0b5d48e2c6a1f0d9"
	// remoteTriage may resolve groups, like a QA engineer's token.
	remoteTriage = "c6a1f0d9b7e4c2a83f9c1e7a0b5d48e2"
)

// remoteAudit receives the audit log of remoteServer.
var remoteAudit bytes.Buffer

type bearerTransport string

func (token bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(token))
	return http.DefaultTransport.RoundTrip(r)
}

// remoteServer serves the query API and MCP behind token authentication with
// one recorded failure, like a deployed dashboard listener.
func remoteServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	start := time.Now().UTC().Add(-time.Minute)
	if err := store.WriteSpans(t.Context(), start, []telemetry.Span{{
		TraceID: telemetry.TraceID{7}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
		ServiceName: "checkout", Name: "POST /pay", StartTime: start, EndTime: start.Add(time.Millisecond),
		StatusCode: telemetry.StatusError, Attributes: telemetry.Attributes{"http.response.status_code": 503},
	}}); err != nil {
		t.Fatal(err)
	}
	token, err := access.ParseToken(remoteAdmin)
	if err == nil {
		token, err = token.WithReadTokens("agent=" + remoteRead)
	}
	if err == nil {
		token, err = token.WithTriageTokens("qa=" + remoteTriage)
	}
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Register(mux, store)
	mux.Handle("/mcp", mcpHandler(store, store, true, audit.New(&remoteAudit)))
	server := httptest.NewServer(token.Protect(mux))
	t.Cleanup(server.Close)
	return server
}

func TestRemoteMCPWithReadToken(t *testing.T) {
	server := remoteServer(t)
	connect := func(token string) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		return client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport(token)}, MaxRetries: -1,
		}, nil)
	}
	if session, err := connect("wrong-token"); err == nil {
		session.Close()
		t.Fatal("connected without a valid token")
	}
	session, err := connect(remoteRead)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_failures", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("list_failures = %+v, %v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(data), "07000000000000000000000000000000") {
		t.Fatalf("structured content %s, %v", data, err)
	}
}

func TestMCPRequiresLoopbackHostWithoutToken(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := mcpHandler(store, nil, false, nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for host, want := range map[string]int{"evil.example": http.StatusForbidden, "127.0.0.1:4318": http.StatusOK} {
		r := httptest.NewRequest(http.MethodPost, "http://"+host+"/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %s: status %d, want %d: %s", host, w.Code, want, w.Body)
		}
	}
}

func TestFailuresFromServer(t *testing.T) {
	server := remoteServer(t)
	t.Setenv("GEKO_TOKEN", "")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"failures", "--json", "--server", server.URL}, &out); err == nil || !strings.Contains(err.Error(), "GEKO_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
	t.Setenv("GEKO_TOKEN", remoteRead)
	if err := run(t.Context(), []string{"failures", "--json", "--server", server.URL + "/", "--service", "checkout"}, &out); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []struct {
			TraceID string `json:"trace_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].TraceID != "07000000000000000000000000000000" {
		t.Fatalf("output %s, %v", out.Bytes(), err)
	}
	if err := run(t.Context(), []string{"failures", "--json", "--server", "geko.example.com"}, &out); err == nil {
		t.Fatal("accepted a server without a scheme")
	}
}

func connectMCP(t *testing.T, server *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport(token)}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !result.IsError {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil || json.Unmarshal(data, out) != nil {
			t.Fatalf("%s returned %s, %v", name, data, err)
		}
	}
	return result
}

func TestRemoteMCPTriageAndComparisons(t *testing.T) {
	server := remoteServer(t)
	toolNames := func(session *mcp.ClientSession) string {
		tools, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		return strings.Join(names, ",")
	}
	reader, admin := connectMCP(t, server, remoteRead), connectMCP(t, server, remoteAdmin)
	triager := connectMCP(t, server, remoteTriage)
	if names := toolNames(reader); strings.Contains(names, "set_group_state") || !strings.Contains(names, "compare_versions") {
		t.Fatalf("read token tools = %s", names)
	}
	if names := toolNames(admin); !strings.Contains(names, "set_group_state") {
		t.Fatalf("access token tools = %s", names)
	}
	if names := toolNames(triager); !strings.Contains(names, "set_group_state") {
		t.Fatalf("triage token tools = %s", names)
	}

	var compared struct {
		Trace struct {
			TraceID string `json:"trace_id"`
		} `json:"trace"`
		Baseline *json.RawMessage `json:"successful_baseline"`
	}
	callTool(t, reader, "compare_trace", map[string]any{"trace_id": "07000000000000000000000000000000"}, &compared)
	if compared.Trace.TraceID != "07000000000000000000000000000000" || compared.Baseline != nil {
		t.Fatalf("compare_trace = %+v", compared)
	}
	var versions struct {
		Comparisons []any `json:"comparisons"`
		MinSamples  int   `json:"min_samples"`
	}
	callTool(t, reader, "compare_versions", map[string]any{"baseline": "1.0", "candidate": "1.1"}, &versions)
	if versions.Comparisons == nil || versions.MinSamples == 0 {
		t.Fatalf("compare_versions = %+v", versions)
	}
	if result := callTool(t, reader, "compare_versions", map[string]any{"baseline": "1.0", "candidate": "1.0"}, nil); !result.IsError {
		t.Fatal("compare_versions accepted identical versions")
	}

	var groups struct {
		Items []struct {
			GroupID string `json:"group_id"`
			State   string `json:"state"`
		} `json:"items"`
	}
	callTool(t, admin, "list_failure_groups", map[string]any{}, &groups)
	if len(groups.Items) != 1 || groups.Items[0].State != "open" || groups.Items[0].GroupID == "" {
		t.Fatalf("groups = %+v", groups)
	}
	if result := callTool(t, admin, "set_group_state", map[string]any{"group_id": groups.Items[0].GroupID, "state": "reopened"}, nil); !result.IsError {
		t.Fatal("set_group_state accepted a derived state")
	}
	remoteAudit.Reset()
	callTool(t, triager, "set_group_state", map[string]any{"group_id": groups.Items[0].GroupID, "state": "resolved", "version": "2.0"}, nil)
	var resolved struct {
		Items []struct {
			State     string `json:"state"`
			ChangedBy string `json:"state_changed_by"`
		} `json:"items"`
	}
	callTool(t, reader, "list_failure_groups", map[string]any{"state": "resolved"}, &resolved)
	if len(resolved.Items) != 1 || resolved.Items[0].State != "resolved" || resolved.Items[0].ChangedBy != "qa" {
		t.Fatalf("resolved groups = %+v", resolved)
	}
	if line := remoteAudit.String(); !strings.Contains(line, `"action":"group.state","actor":"qa","role":"triager","via":"token"`) ||
		!strings.Contains(line, `"client":"mcp"`) || !strings.Contains(line, `"version":"2.0"`) {
		t.Fatalf("audit = %s", line)
	}
}
