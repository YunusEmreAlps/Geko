package main

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestMCPAnswersMalformedLinesAndContinues(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "geko.db")
	writer, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	reader, err := sqlite.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveMCP(ctx, reader, inReader, outWriter)
		outWriter.Close()
	}()
	responses := bufio.NewScanner(outReader)
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(inWriter, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	type response struct {
		ID     json.RawMessage `json:"id"`
		Result *struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	next := func() response {
		t.Helper()
		if !responses.Scan() {
			t.Fatalf("server stopped: %v", responses.Err())
		}
		var got response
		if err := json.Unmarshal(responses.Bytes(), &got); err != nil {
			t.Fatalf("invalid response %s: %v", responses.Bytes(), err)
		}
		return got
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if got := next(); got.Result == nil || got.Result.Capabilities["logging"] != nil ||
		string(got.Result.Capabilities["tools"]) != "{}" {
		t.Fatalf("initialize = %+v", got.Result)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	for _, tc := range []struct {
		line string
		code int
		id   string
	}{
		{`{bad`, -32700, "null"},
		{`{"jsonrpc":"1.0","id":7,"method":"ping"}`, -32600, "7"},
		{`{"jsonrpc":"2.0","id":{},"method":"ping"}`, -32600, "null"},
		{`[]`, -32600, "null"},
	} {
		send(tc.line)
		if got := next(); got.Error == nil || got.Error.Code != tc.code || string(got.ID) != tc.id {
			t.Fatalf("%s: response = %+v, id %s", tc.line, got.Error, got.ID)
		}
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if got := next(); string(got.ID) != "2" || got.Error != nil {
		t.Fatalf("ping after malformed input = %+v", got)
	}
	inWriter.Close()
	if err := <-done; err != nil && err != io.EOF {
		t.Fatalf("server error: %v", err)
	}
}
