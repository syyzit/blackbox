package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecideFirstMatchWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	body := `{
	  "default": "allow",
	  "rules": [
	    {"tool": "write_*", "action": "require_approval", "reason": "writes need a human"},
	    {"server": "*filesystem*", "tool": "shell", "action": "deny"},
	    {"tool": "read_*", "action": "allow"}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		req  Request
		want Action
	}{
		{Request{Server: "filesystem", Tool: "write_file"}, RequireApproval},
		{Request{Server: "filesystem", Tool: "shell"}, Deny},
		{Request{Server: "filesystem", Tool: "read_file"}, Allow},
		{Request{Server: "other", Tool: "anything"}, Allow},
	}
	for _, c := range cases {
		if got := p.Decide(c.req); got.Action != c.want {
			t.Errorf("%s/%s = %s, want %s", c.req.Server, c.req.Tool, got.Action, c.want)
		}
	}
}

func TestArgsRegex(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "*", Args: `"path"\s*:\s*"/etc/`, Action: Deny, Reason: "system path"},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.Decide(Request{Tool: "read", Args: `{"path":"/etc/passwd"}`}); got.Action != Deny {
		t.Fatalf("expected deny, got %s", got.Action)
	}
	if got := p.Decide(Request{Tool: "read", Args: `{"path":"/tmp/x"}`}); got.Action != Allow {
		t.Fatalf("expected allow, got %s", got.Action)
	}
}

func TestMissingFileIsPermissive(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide(Request{Tool: "anything"}); got.Action != Allow {
		t.Fatalf("expected allow, got %s", got.Action)
	}
	if !p.RugPullBlock {
		t.Fatal("default policy should still block rug-pulls")
	}
}

func TestRejectsUnknownAction(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{{Tool: "x", Action: "maybe"}}}
	if err := p.validate(); err == nil {
		t.Fatal("expected an error for an unknown action")
	}
}

func TestWildcard(t *testing.T) {
	if !wildcard("*", "anything") || !wildcard("read_*", "read_file") {
		t.Fatal("wildcards should match")
	}
	if wildcard("read_*", "write_file") || wildcard("a?c", "abbc") {
		t.Fatal("wildcards should not over-match")
	}
}
