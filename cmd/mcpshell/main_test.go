package main

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *server {
	t.Helper()
	return &server{
		root:      t.TempDir(),
		allow:     splitList("echo,ls,git"),
		timeout:   5 * time.Second,
		maxOutput: 4096,
	}
}

func TestExecuteRunsAllowlistedCommand(t *testing.T) {
	s := testServer(t)
	text, failed := s.execute(callArgs{Command: "echo", Args: []string{"hello", "world"}})
	if failed {
		t.Fatalf("echo failed: %s", text)
	}
	if !strings.Contains(text, "hello world") {
		t.Fatalf("missing output: %s", text)
	}
	if !strings.Contains(text, "exit 0") {
		t.Fatalf("missing exit code: %s", text)
	}
}

func TestExecuteRejectsUnknownCommand(t *testing.T) {
	s := testServer(t)
	text, failed := s.execute(callArgs{Command: "sh", Args: []string{"-c", "rm -rf /"}})
	if !failed || !strings.Contains(text, "not allowlisted") {
		t.Fatalf("expected a rejection, got failed=%v text=%s", failed, text)
	}
}

func TestExecuteRejectsCwdEscape(t *testing.T) {
	s := testServer(t)
	text, failed := s.execute(callArgs{Command: "echo", Cwd: "../.."})
	if !failed || !strings.Contains(text, "outside the repo root") {
		t.Fatalf("expected a cwd rejection, got failed=%v text=%s", failed, text)
	}

	text, failed = s.execute(callArgs{Command: "echo", Cwd: "/etc"})
	if !failed || !strings.Contains(text, "outside the repo root") {
		t.Fatalf("expected an absolute cwd rejection, got failed=%v text=%s", failed, text)
	}
}

func TestExecuteRestrictsGitToReadOnly(t *testing.T) {
	s := testServer(t)
	text, failed := s.execute(callArgs{Command: "git", Args: []string{"push", "--force"}})
	if !failed || !strings.Contains(text, "read-only") {
		t.Fatalf("expected git push to be rejected, got failed=%v text=%s", failed, text)
	}
}

func TestProtocolRoundTrip(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := testServer(t)

	done := make(chan error, 1)
	go func() { done <- s.serve(inR, outW) }()
	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})

	send := func(v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		line, err := bufio.NewReader(outR).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	if res := read()["result"].(map[string]any); res["serverInfo"] == nil {
		t.Fatalf("bad initialize response: %+v", res)
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	var tools []any
	if res, ok := read()["result"].(map[string]any); ok {
		tools, _ = res["tools"].([]any)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %+v", tools)
	}

	send(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "run_command",
			"arguments": map[string]any{"command": "echo", "args": []string{"protocol"}},
		},
	})
	res := read()["result"].(map[string]any)
	content := res["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "protocol") {
		t.Fatalf("tool call did not run: %+v", res)
	}
}
