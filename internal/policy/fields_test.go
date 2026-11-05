package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mcpArgs(t *testing.T, arguments map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": "tool", "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFieldRules(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "shell", Match: map[string]string{"command": "rm *"}, Action: Deny, Reason: "rm"},
		{Tool: "shell", Match: map[string]string{"args.*": "--force"}, Action: Deny, Reason: "force"},
		{Tool: "write", Paths: map[string]string{"path": "/etc/*"}, Action: Deny, Reason: "etc"},
		{Tool: "write", Paths: map[string]string{"path": "C:/Windows/*"}, Action: Deny, Reason: "windows"},
		{Tool: "fetch", Match: map[string]string{"opts.retries": "1?"}, Action: RequireApproval},
		{Tool: "both", Match: map[string]string{"a": "x"}, Paths: map[string]string{"p": "/secret/*"}, Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}

	winPath := strings.ReplaceAll("C:/Windows/System32/drivers/etc/hosts", "/", string(rune(92)))
	cases := []struct {
		tool string
		args map[string]any
		want Action
	}{
		{"shell", map[string]any{"command": "rm -rf /"}, Deny},
		{"shell", map[string]any{"command": "ls"}, Allow},
		{"shell", map[string]any{"command": "git", "args": []any{"push", "--force"}}, Deny},
		{"shell", map[string]any{"command": "git", "args": []any{"push"}}, Allow},
		{"write", map[string]any{"path": "/etc/passwd"}, Deny},
		{"write", map[string]any{"path": "/tmp/../etc/passwd"}, Deny},
		{"write", map[string]any{"path": "//etc//shadow"}, Deny},
		{"write", map[string]any{"path": "/tmp/etc/x"}, Allow},
		{"write", map[string]any{"path": winPath}, Deny},
		{"write", map[string]any{"other": "/etc/passwd"}, Allow}, // missing field never matches
		{"fetch", map[string]any{"opts": map[string]any{"retries": 12}}, RequireApproval},
		{"fetch", map[string]any{"opts": map[string]any{"retries": 3}}, Allow},
		{"both", map[string]any{"a": "x", "p": "/secret/k"}, Deny},
		{"both", map[string]any{"a": "x", "p": "/public/k"}, Allow}, // every condition must hold
	}
	for _, c := range cases {
		got := p.Decide(Request{Tool: c.tool, Args: mcpArgs(t, c.args)})
		if got.Action != c.want {
			t.Errorf("%s %v: got %s, want %s", c.tool, c.args, got.Action, c.want)
		}
	}
}

func TestFieldRulesUseExplicitArguments(t *testing.T) {
	// Built-in tools pass their input directly rather than MCP params.
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "edit", Paths: map[string]string{"filePath": "*/.env"}, Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"filePath":"/repo/app/../.env","oldString":"a"}`)
	if got := p.Decide(Request{Tool: "edit", Args: string(input), Arguments: input}); got.Action != Deny {
		t.Fatalf("built-in input not matched: %s", got.Action)
	}
}

func TestFieldRulesLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`{"rules":[{"tool":"write","paths":{"path":"/etc/*"},"action":"deny"}]}`), 0o600)
	p, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide(Request{Tool: "write", Args: `{"arguments":{"path":"/etc/x"}}`}); got.Action != Deny {
		t.Fatalf("loaded field rule did not apply: %s", got.Action)
	}

	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"rules":[{"match":{"a..b":"x"},"action":"deny"}]}`), 0o600)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "empty segment") {
		t.Fatalf("bad field path accepted: %v", err)
	}
}

func FuzzFieldRules(f *testing.F) {
	f.Add(`{"arguments":{"a":{"b":[1,"x",null]}}}`, "a.b.*", "x")
	f.Add(`{"arguments":[1,2]}`, "0", "*")
	f.Fuzz(func(t *testing.T, args, path, glob string) {
		p := &Policy{Default: Allow, Rules: []Rule{
			{Match: map[string]string{path: glob}, Action: Deny},
			{Paths: map[string]string{path: glob}, Action: Deny},
		}}
		if p.validate() != nil {
			return
		}
		_ = p.Decide(Request{Tool: "t", Args: args}) // must not panic
	})
}
