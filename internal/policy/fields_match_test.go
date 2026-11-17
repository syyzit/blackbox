package policy

import (
	"strings"
	"testing"
)

// A glob has to see through newlines: "*" means any run of characters, and a value
// that carries a line break must not slip past a deny rule.
func TestGlobsMatchAcrossNewlines(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "shell", Match: map[string]string{"command": "*rm -rf*"}, Action: Deny},
		{Tool: "shell", Match: map[string]string{"command": "a?b"}, Action: Deny},
		{Tool: "read", Paths: map[string]string{"path": "/etc/*"}, Action: Deny},
		{Tool: "multi*line", Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"shell", map[string]any{"command": "echo hi\nrm -rf /"}},
		{"shell", map[string]any{"command": "a\nb"}},
		{"read", map[string]any{"path": "/etc/passwd\n"}},
		{"multi\nline", map[string]any{}},
	}
	for _, c := range cases {
		if got := p.Decide(Request{Tool: c.tool, Args: mcpArgs(t, c.args)}); got.Action != Deny {
			t.Errorf("%q %v: got %s, want deny", c.tool, c.args, got.Action)
		}
	}
	if !wildcard("a*z", "a\nz") {
		t.Error("uncompiled wildcard does not match across a newline")
	}
}

// Field rules must read the same "arguments" member the server will. Go's struct
// decoding matches keys case-insensitively, which let a decoy "Arguments" member
// replace the real one.
func TestFieldRulesReadArgumentsKeyExactly(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "read_file", Paths: map[string]string{"path": "/etc/*"}, Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args string
		want Action
	}{
		{`{"name":"read_file","arguments":{"path":"/etc/passwd"},"Arguments":{"path":"/tmp/ok"}}`, Deny},
		{`{"name":"read_file","ARGUMENTS":{"path":"/tmp/ok"},"arguments":{"path":"/etc/passwd"}}`, Deny},
		{`{"name":"read_file","Arguments":{"path":"/etc/passwd"}}`, Allow}, // not the member a server reads
		{`{"name":"read_file","arguments":{"path":"/tmp/ok"}}`, Allow},
	}
	for _, c := range cases {
		if got := p.Decide(Request{Tool: "read_file", Args: c.args}); got.Action != c.want {
			t.Errorf("%s: got %s, want %s", c.args, got.Action, c.want)
		}
	}
}

// Values are normalised before matching, so a paths pattern has to be normalised the
// same way or a rule written with backslashes or a trailing slash can never match.
func TestPathsPatternsAreNormalised(t *testing.T) {
	bs := string(rune(92))
	win := func(s string) string { return strings.ReplaceAll(s, "/", bs) }
	cases := []struct {
		pattern, value string
		want           Action
	}{
		{win("C:/Users/me/.ssh/*"), win("C:/Users/me/.ssh/id_rsa"), Deny},
		{win("C:/Users/me/.ssh/*"), "C:/Users/me/.ssh/id_rsa", Deny},
		{win("C:/Users/me/.ssh/*"), "C:/Users/me/ssh/id_rsa", Allow},
		// A backslash before a wildcard is a separator, not an escape.
		{win("C:/Users/me/.ssh/") + "?d_rsa", "C:/Users/me/.ssh/id_rsa", Deny},
		{"/etc/", "/etc/", Deny},
		{"/etc/", "/etc", Deny},
		{"/etc/", "/etc/passwd", Allow},
		{"/etc//", "/etc", Deny},
		{"/", "/", Deny},
		{"/", "/etc", Allow},
		{bs, "/", Deny},
		{"/etc/*", "/etc/passwd", Deny},
	}
	for _, c := range cases {
		p := &Policy{Default: Allow, Rules: []Rule{
			{Paths: map[string]string{"path": c.pattern}, Action: Deny},
		}}
		if err := p.validate(); err != nil {
			t.Fatal(err)
		}
		got := p.Decide(Request{Tool: "t", Args: mcpArgs(t, map[string]any{"path": c.value})})
		if got.Action != c.want {
			t.Errorf("paths %q against %q: got %s, want %s", c.pattern, c.value, got.Action, c.want)
		}
	}

	// match is not a path rule: its pattern stays literal.
	p := &Policy{Default: Allow, Rules: []Rule{
		{Match: map[string]string{"text": "a" + bs + "b/"}, Action: Deny},
	}}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.Decide(Request{Tool: "t", Args: mcpArgs(t, map[string]any{"text": "a" + bs + "b/"})}); got.Action != Deny {
		t.Errorf("match pattern was rewritten: %s", got.Action)
	}
}
