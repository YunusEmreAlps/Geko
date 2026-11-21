package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// mcpClients maps each supported client to its project configuration file
// and the key holding its servers.
var mcpClients = map[string]struct{ file, key string }{
	"claude-code": {".mcp.json", "mcpServers"},
	"cursor":      {filepath.Join(".cursor", "mcp.json"), "mcpServers"},
	"vscode":      {filepath.Join(".vscode", "mcp.json"), "servers"},
}

const mcpSetupUsage = "usage: geko mcp setup claude-code|cursor|vscode [--server URL] [--print]"

// mcpSetupCommand registers Geko in an MCP client's project configuration,
// keeping every other server already listed there.
func mcpSetupCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "setup" {
		return errors.New(mcpSetupUsage)
	}
	flags := flag.NewFlagSet("mcp setup", flag.ContinueOnError)
	flags.SetOutput(out)
	server := flags.String("server", "", "Geko server URL; the client sends GEKO_TOKEN as a read token")
	printOnly := flags.Bool("print", false, "print the configuration instead of writing it")
	if len(args) < 2 {
		return errors.New(mcpSetupUsage)
	}
	name := args[1]
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	client, ok := mcpClients[name]
	if !ok || flags.NArg() != 0 {
		return errors.New(mcpSetupUsage)
	}
	entry, inputs, err := mcpEntry(name, *server)
	if err != nil {
		return err
	}
	if *printOnly {
		snippet := map[string]any{client.key: map[string]any{"geko": entry}}
		if inputs != nil {
			snippet["inputs"] = inputs
		}
		data, err := json.MarshalIndent(snippet, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", data)
		return err
	}
	config := map[string]any{}
	existing, err := os.ReadFile(client.file)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(existing, &config); err != nil {
			return fmt.Errorf("%s is not plain JSON (%v); add the output of geko mcp setup %s --print to it yourself", client.file, err, name)
		}
	}
	servers, _ := config[client.key].(map[string]any)
	if servers == nil {
		if _, present := config[client.key]; present {
			return fmt.Errorf("%s in %s is not an object; fix it before adding Geko", client.key, client.file)
		}
		servers = map[string]any{}
	}
	_, replaced := servers["geko"]
	servers["geko"] = entry
	config[client.key] = servers
	if inputs != nil {
		config["inputs"] = mergeInputs(config["inputs"], inputs)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(client.file), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(client.file, data.Bytes(), 0o644); err != nil {
		return err
	}
	action := "Added"
	if replaced {
		action = "Updated"
	}
	fmt.Fprintf(out, "%s the geko server in %s.\n", action, client.file)
	switch {
	case *server != "" && name == "vscode":
		fmt.Fprintln(out, "VS Code asks for a Geko read token the first time it starts the server.")
	case *server != "":
		fmt.Fprintln(out, "Set GEKO_TOKEN to a Geko read token in the environment that starts the client.")
	default:
		fmt.Fprintln(out, "The client starts geko mcp, which reads the local database read-only.")
	}
	fmt.Fprintln(out, "Restart the client or reload its MCP servers, then ask it to investigate the open error groups.")
	return nil
}

// mcpEntry describes the geko server for one client, with any VS Code input
// prompts it needs.
func mcpEntry(client, server string) (map[string]any, []any, error) {
	if server != "" {
		base, err := remoteServerURL(server)
		if err != nil {
			return nil, nil, err
		}
		endpoint := base.String() + "/mcp"
		switch client {
		case "vscode":
			return map[string]any{"type": "http", "url": endpoint, "headers": map[string]any{"Authorization": "Bearer ${input:geko-token}"}},
				[]any{map[string]any{"type": "promptString", "id": "geko-token", "description": "Geko read token", "password": true}}, nil
		case "cursor":
			return map[string]any{"url": endpoint, "headers": map[string]any{"Authorization": "Bearer ${env:GEKO_TOKEN}"}}, nil, nil
		default:
			return map[string]any{"type": "http", "url": endpoint, "headers": map[string]any{"Authorization": "Bearer ${GEKO_TOKEN}"}}, nil, nil
		}
	}
	entry := map[string]any{"command": gekoCommand(), "args": []any{"mcp"}}
	if client == "vscode" {
		entry["type"] = "stdio"
	}
	environment := map[string]any{}
	for _, name := range []string{"GEKO_DB_PATH", "GEKO_CONFIG"} {
		if value := os.Getenv(name); value != "" {
			if absolute, err := filepath.Abs(value); err == nil {
				value = absolute
			}
			environment[name] = value
		}
	}
	if len(environment) > 0 {
		entry["env"] = environment
	}
	return entry, nil, nil
}

// gekoCommand is this executable's absolute path, or plain geko when it is a
// temporary build from go run.
func gekoCommand() string {
	executable, err := os.Executable()
	if err != nil {
		return "geko"
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	temporary, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		temporary = os.TempDir()
	}
	if strings.HasPrefix(strings.ToLower(executable), strings.ToLower(temporary)) {
		return "geko"
	}
	return executable
}

// mergeInputs adds VS Code input prompts whose IDs are not listed yet.
func mergeInputs(existing any, added []any) []any {
	inputs, _ := existing.([]any)
	ids := map[any]bool{}
	for _, input := range inputs {
		if object, ok := input.(map[string]any); ok {
			ids[object["id"]] = true
		}
	}
	for _, input := range added {
		if !ids[input.(map[string]any)["id"]] {
			inputs = append(inputs, input)
		}
	}
	return inputs
}
