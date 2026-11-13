package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpHandler serves the read-only investigation tools over Streamable HTTP.
// Without a token, only loopback Host names are served, which blocks DNS
// rebinding; the SDK's own check is disabled because it also rejects a TLS
// proxy on the same host that forwards the public Host name.
//
// With triage, callers with full access (the access token, or any caller when no
// token is configured) also get set_group_state; read tokens never do.
func mcpHandler(store investigationReader, triage groupTriage, tokenEnabled bool) http.Handler {
	readOnly := investigationServer(store)
	var triageServer *mcp.Server
	if triage != nil {
		triageServer = investigationServer(store)
		addTriageTool(triageServer, triage)
	}
	choose := func(r *http.Request) *mcp.Server {
		if triageServer != nil && (!tokenEnabled || access.FullAccess(r.Context())) {
			return triageServer
		}
		return readOnly
	}
	handler := mcp.NewStreamableHTTPHandler(choose, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, DisableLocalhostProtection: true, MaxRequestBodyBytes: maxProtocolLine,
	})
	protected := http.NewCrossOriginProtection().Handler(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !api.LocalHost(r.Host) && !access.Authenticated(r.Context()) {
			http.Error(w, "MCP requires a loopback Host or a token", http.StatusForbidden)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

// remoteFailures reads failures from a Geko server's query API. The token comes
// from GEKO_TOKEN so it stays out of the process list and shell history.
func remoteFailures(ctx context.Context, server string, args failureArgs) (queryPage[investigation.TraceSummary], error) {
	var result queryPage[investigation.TraceSummary]
	base, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.Host == "" || base.RawQuery != "" || base.User != nil {
		return result, errors.New("--server must be an http or https URL, such as https://geko.example.com")
	}
	params := args.params()
	for key, values := range params {
		if len(values) == 1 && values[0] == "" {
			params.Del(key)
		}
	}
	endpoint := base.JoinPath("api", "v1", "failures")
	endpoint.RawQuery = params.Encode()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result, err
	}
	if token := os.Getenv("GEKO_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("query %s: %w", base.Redacted(), err)
	}
	defer response.Body.Close()
	body := io.LimitReader(response.Body, 16<<20)
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(body).Decode(&failure)
		message := failure.Error
		if response.StatusCode == http.StatusUnauthorized {
			message = "set GEKO_TOKEN to a read token or the access token"
		}
		return result, fmt.Errorf("query %s: %s %s", base.Redacted(), response.Status, message)
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return result, fmt.Errorf("decode response from %s: %w", base.Redacted(), err)
	}
	return result, nil
}
