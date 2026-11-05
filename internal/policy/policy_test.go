package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestArgsRegexSeesThroughJSONEscapes(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "*", Args: `rm -rf`, Action: Deny},
		{Tool: "*", Args: `"path":"/etc/`, Action: Deny},
		{Tool: "*", Args: `<script>`, Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"name":"shell","arguments":{"command":"rm\u0020-rf /"}}`,
		`{"name":"shell","arguments":{"command":"\u0072m -rf /"}}`,
		`{"name":"read","arguments":{"path" : "\/etc\/passwd"}}`,
		`{"name":"read","arguments":{"path":"\u002fetc/shadow"}}`,
		`{"name":"write","arguments":{"text":"\u003cscript>"}}`,
	} {
		if got := p.Decide(Request{Tool: "x", Args: args}); got.Action != Deny {
			t.Errorf("escaped args slipped past the rule: %s -> %s", args, got.Action)
		}
	}
	if got := p.Decide(Request{Tool: "x", Args: `{"arguments":{"n":12345678901234567890}}`}); got.Action != Allow {
		t.Fatalf("large number: %s", got.Action)
	}
}

func TestCanonicalArgsKeepsNonJSON(t *testing.T) {
	for _, in := range []string{"", "not json", `{"a":1} trailing`} {
		if got := CanonicalArgs(in); got != in {
			t.Errorf("CanonicalArgs(%q) = %q", in, got)
		}
	}
	if got := CanonicalArgs(`{"n": 12345678901234567890}`); got != `{"n":12345678901234567890}` {
		t.Errorf("number precision lost: %s", got)
	}
}

func TestShadowModeParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"mode":"shadow","rules":[{"tool":"shell","action":"deny"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeShadow {
		t.Fatalf("mode = %q", p.Mode)
	}
	if got := p.Decide(Request{Tool: "shell"}); got.Action != Deny {
		t.Fatalf("decision = %s", got.Action)
	}
}

func TestRejectsUnknownMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"mode":"maybe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an unknown mode")
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

type slowApprover struct {
	delay time.Duration
	allow bool
}

func (s slowApprover) Approve(ApprovalRequest) bool {
	time.Sleep(s.delay)
	return s.allow
}

func TestTimeoutApproverDeniesSlowAnswer(t *testing.T) {
	got := TimeoutApprover{
		Inner:   slowApprover{delay: 200 * time.Millisecond, allow: true},
		Timeout: 20 * time.Millisecond,
	}.Approve(ApprovalRequest{Tool: "write_file"})
	if got {
		t.Fatal("a slow answer must be a denial")
	}
}

func TestTimeoutApproverPassesFastAnswer(t *testing.T) {
	got := TimeoutApprover{
		Inner:   slowApprover{delay: time.Millisecond, allow: true},
		Timeout: time.Second,
	}.Approve(ApprovalRequest{Tool: "write_file"})
	if !got {
		t.Fatal("a fast yes must be honoured")
	}
}

// FuzzCanonicalArgs checks the property the gate relies on: canonicalizing is stable,
// keeps valid JSON valid, and does not change what the JSON means.
func FuzzCanonicalArgs(f *testing.F) {
	for _, s := range []string{
		`{"name":"shell","arguments":{"command":"rm\u0020-rf /"}}`,
		`{"path" : "\/etc\/passwd", "n": 12345678901234567890, "f": 1.50}`,
		`[1, "a", null, true, {"<": ">&"}]`,
		`"just a string"`,
		`not json`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := CanonicalArgs(in)
		if again := CanonicalArgs(out); again != out {
			t.Fatalf("not stable: %q -> %q -> %q", in, out, again)
		}
		if !json.Valid([]byte(in)) {
			return
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("valid input became invalid: %q -> %q", in, out)
		}
		var a, b any
		if json.Unmarshal([]byte(in), &a) == nil && json.Unmarshal([]byte(out), &b) == nil {
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("meaning changed: %q -> %q", in, out)
			}
		}
	})
}
