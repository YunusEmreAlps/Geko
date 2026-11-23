package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/config"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return value
}

func TestMCPSetupMergesIntoEachClient(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GEKO_DB_PATH", "data/geko.db")
	t.Setenv("GEKO_CONFIG", "")
	if err := os.WriteFile(".mcp.json", []byte(`{"mcpServers":{"other":{"command":"other"}},"keep":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"mcp", "setup", "claude-code"}, &out); err != nil {
		t.Fatal(err)
	}
	config := readJSON(t, ".mcp.json")
	servers := config["mcpServers"].(map[string]any)
	geko := servers["geko"].(map[string]any)
	if servers["other"] == nil || config["keep"] != true || geko["args"].([]any)[0] != "mcp" ||
		!filepath.IsAbs(geko["env"].(map[string]any)["GEKO_DB_PATH"].(string)) || !strings.Contains(out.String(), "Added") {
		t.Fatalf("claude-code config = %v\n%s", config, out.String())
	}
	out.Reset()
	if err := run(t.Context(), []string{"mcp", "setup", "claude-code", "--server", "https://geko.example.com/"}, &out); err != nil {
		t.Fatal(err)
	}
	geko = readJSON(t, ".mcp.json")["mcpServers"].(map[string]any)["geko"].(map[string]any)
	if geko["url"] != "https://geko.example.com/mcp" || geko["headers"].(map[string]any)["Authorization"] != "Bearer ${GEKO_TOKEN}" ||
		!strings.Contains(out.String(), "Updated") {
		t.Fatalf("remote claude-code entry = %v", geko)
	}

	if err := run(t.Context(), []string{"mcp", "setup", "cursor", "--server", "https://geko.example.com"}, &out); err != nil {
		t.Fatal(err)
	}
	cursor := readJSON(t, filepath.Join(".cursor", "mcp.json"))["mcpServers"].(map[string]any)["geko"].(map[string]any)
	if cursor["headers"].(map[string]any)["Authorization"] != "Bearer ${env:GEKO_TOKEN}" {
		t.Fatalf("cursor entry = %v", cursor)
	}

	for range 2 { // a second run keeps a single input prompt
		if err := run(t.Context(), []string{"mcp", "setup", "vscode", "--server", "https://geko.example.com"}, &out); err != nil {
			t.Fatal(err)
		}
	}
	vscode := readJSON(t, filepath.Join(".vscode", "mcp.json"))
	if entry := vscode["servers"].(map[string]any)["geko"].(map[string]any); entry["type"] != "http" || len(vscode["inputs"].([]any)) != 1 {
		t.Fatalf("vscode config = %v", vscode)
	}

	if err := os.WriteFile(filepath.Join(".vscode", "mcp.json"), []byte("{ // comments\n}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"mcp", "setup", "vscode"}, &out); err == nil || !strings.Contains(err.Error(), "--print") {
		t.Fatalf("JSON with comments = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(".vscode", "mcp.json")); string(data) != "{ // comments\n}" {
		t.Fatal("setup overwrote a file it could not parse")
	}
	out.Reset()
	if err := run(t.Context(), []string{"mcp", "setup", "vscode", "--print"}, &out); err != nil || !strings.Contains(out.String(), `"type": "stdio"`) {
		t.Fatalf("print = %v\n%s", err, out.String())
	}
	for _, args := range [][]string{{"mcp", "setup"}, {"mcp", "setup", "emacs"}, {"mcp", "install", "cursor"}, {"mcp", "setup", "cursor", "--server", "ftp://x"}} {
		if err := run(t.Context(), args, &out); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
}

// A client that starts geko mcp in another directory reads the same database
// as setup resolved, from an implicit geko.yaml with a relative db_path.
func TestMCPSetupPinsTheEffectiveConfiguration(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	t.Setenv("GEKO_DB_PATH", "")
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_TOKENS_FILE", "")
	t.Setenv("GEKO_ACCESS_TOKEN", "secret-token-value-0123456789abcdefghij")
	if err := os.WriteFile("geko.yaml", []byte("db_path: data/geko.db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"mcp", "setup", "claude-code"}, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(".mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data)+out.String(), "secret-token-value-0123456789abcdefghij") {
		t.Fatalf("setup wrote a token value: %s", data)
	}
	environment := readJSON(t, ".mcp.json")["mcpServers"].(map[string]any)["geko"].(map[string]any)["env"].(map[string]any)
	want, err := filepath.Abs(filepath.Join(project, "data", "geko.db"))
	if err != nil {
		t.Fatal(err)
	}
	configPath, _ := filepath.Abs(filepath.Join(project, "geko.yaml"))
	if environment["GEKO_DB_PATH"] != want || environment["GEKO_CONFIG"] != configPath {
		t.Fatalf("env = %v, want database %s and config %s", environment, want, configPath)
	}

	t.Chdir(t.TempDir())
	for name, value := range environment {
		t.Setenv(name, value.(string))
	}
	cfg, err := config.Load()
	if err != nil || cfg.DBPath != want {
		t.Fatalf("client in another directory reads %q, want %q (%v)", cfg.DBPath, want, err)
	}
}
