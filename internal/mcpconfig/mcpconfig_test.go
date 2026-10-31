package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWrapClaudeStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	original := `{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "server-filesystem", "/tmp"],
      "env": {"TOKEN": "keep-me"}
    },
    "remote": {"url": "https://example.com/mcp"}
  },
  "unrelated": {"keep": true}
}`
	write(t, path, original)

	cfg, err := Load(Location{Path: path, Client: "Claude Code", Style: StyleMcpServers})
	if err != nil {
		t.Fatal(err)
	}
	servers, err := cfg.Servers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %+v", servers)
	}
	if servers[0].Name != "fs" || servers[0].Wrapped || servers[0].Remote {
		t.Fatalf("unexpected fs entry: %+v", servers[0])
	}
	if !servers[1].Remote {
		t.Fatalf("remote entry not detected: %+v", servers[1])
	}

	before, after, err := cfg.Wrap("fs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(before, "npx -y") || !strings.HasPrefix(after, "blackbox run -- npx -y") {
		t.Fatalf("unexpected rewrite: %q -> %q", before, after)
	}
	if !cfg.Changed() {
		t.Fatal("config should be marked changed")
	}

	backup, err := cfg.Write()
	if err != nil {
		t.Fatal(err)
	}
	gotBackup, _ := os.ReadFile(backup)
	if string(gotBackup) != original {
		t.Fatal("backup does not match the original")
	}

	updated, _ := os.ReadFile(path)
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatalf("rewritten config is not JSON: %v", err)
	}
	serversMap := root["mcpServers"].(map[string]any)
	fs := serversMap["fs"].(map[string]any)
	if fs["command"] != "blackbox" {
		t.Fatalf("command not rewritten: %+v", fs)
	}
	args := fs["args"].([]any)
	want := []string{"run", "--", "npx", "-y", "server-filesystem", "/tmp"}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %v, want %s", i, args[i], want[i])
		}
	}
	if fs["env"].(map[string]any)["TOKEN"] != "keep-me" {
		t.Fatalf("env was not preserved: %+v", fs)
	}
	if !strings.Contains(string(updated), `"keep": true`) {
		t.Fatalf("unrelated formatting was not preserved:\n%s", updated)
	}
}

func TestWrapVSCodeStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{"servers":{"gh":{"type":"stdio","command":"npx","args":["-y","gh-mcp"]}}}`)

	cfg, err := Load(Location{Path: path, Client: "VS Code", Style: StyleServers})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.Wrap("gh"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Write(); err != nil {
		t.Fatal(err)
	}

	var root struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"servers"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	gh := root.Servers["gh"]
	if gh.Type != "stdio" || gh.Command != "blackbox" || gh.Args[0] != "run" || gh.Args[1] != "--" {
		t.Fatalf("bad rewrite: %+v", gh)
	}
}

func TestWrapOpenCodeStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	write(t, path, `{"mcp":{"notes":{"type":"local","command":["npx","-y","notes-mcp"],"enabled":true}}}`)

	cfg, err := Load(Location{Path: path, Client: "OpenCode", Style: StyleOpenCode})
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := cfg.Servers()
	if len(servers) != 1 || servers[0].Command[0] != "npx" {
		t.Fatalf("bad discovery: %+v", servers)
	}
	if _, _, err := cfg.Wrap("notes"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Write(); err != nil {
		t.Fatal(err)
	}

	var root struct {
		MCP map[string]struct {
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	got := root.MCP["notes"]
	want := []string{"blackbox", "run", "--", "npx", "-y", "notes-mcp"}
	if len(got.Command) != len(want) {
		t.Fatalf("command = %v", got.Command)
	}
	for i := range want {
		if got.Command[i] != want[i] {
			t.Fatalf("command = %v, want %v", got.Command, want)
		}
	}
	if !got.Enabled {
		t.Fatal("unrelated field was lost")
	}
}

func TestWrapRejectsRemoteAndWrapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{"mcpServers":{
	  "remote":{"url":"https://x"},
	  "done":{"command":"blackbox","args":["run","--","npx","x"]}
	}}`)
	cfg, err := Load(Location{Path: path, Style: StyleMcpServers})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.Wrap("remote"); err == nil {
		t.Fatal("expected remote to be rejected")
	}
	if _, _, err := cfg.Wrap("done"); err == nil {
		t.Fatal("expected already-wrapped to be rejected")
	}
	if cfg.Changed() {
		t.Fatal("rejected wraps must not modify the config")
	}
}

func TestDottedServerNameIsEscaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{"mcpServers":{"my.server":{"command":"npx","args":["x"]}}}`)
	cfg, err := Load(Location{Path: path, Style: StyleMcpServers})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.Wrap("my.server"); err != nil {
		t.Fatal(err)
	}
	var root struct {
		MCP map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(cfg.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if root.MCP["my.server"].Command != "blackbox" {
		t.Fatalf("dotted name rewrite failed: %s", cfg.Bytes())
	}
}

func TestDetectStyle(t *testing.T) {
	cases := map[string]Style{
		`{"mcpServers":{}}`: StyleMcpServers,
		`{"servers":{}}`:    StyleServers,
		`{"mcp":{}}`:        StyleOpenCode,
	}
	for body, want := range cases {
		got, err := DetectStyle([]byte(body))
		if err != nil || got != want {
			t.Fatalf("%s: got %v, want %v (%v)", body, got, want, err)
		}
	}
}
