package policy

import (
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

// Built-in tool calls recorded by `blackbox ingest` and `blackbox hook` store the bare
// tool input as Args, which is also what the live check evaluates. The simulation has
// to read them the same way or it reports allow where the gate said deny.
func TestEvaluateExternalSessionMatchesFieldRules(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "Read", Paths: map[string]string{"file_path": "/etc/*"}, Action: Deny, Reason: "system path"},
		{Tool: "Bash", Match: map[string]string{"command": "git push*"}, Action: RequireApproval},
		{Tool: "Bash", Args: `"command":"rm `, Action: Deny},
	}}
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "Read", ID: "a", CallID: "a", Args: `{"file_path":"/etc/passwd"}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "Read", ID: "a", CallID: "a", Status: event.StatusOK},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "Read", ID: "b", CallID: "b", Args: `{"file_path":"/tmp/x"}`},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "Bash", ID: "c", CallID: "c", Args: `{"command":"git push origin main"}`},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "Bash", ID: "d", CallID: "d", Args: `{"command":"rm -rf x"}`},
		// A built-in input may itself have an "arguments" member; it is still the input.
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "Read", ID: "e", CallID: "e", Args: `{"file_path":"/etc/shadow","arguments":{"file_path":"/tmp/ok"}}`},
	}
	want := []Action{Deny, Allow, RequireApproval, Deny, Deny}

	sum := EvaluateExternalSession(p, "claude-code", events)
	if len(sum.Impacts) != len(want) {
		t.Fatalf("impacts = %d, want %d", len(sum.Impacts), len(want))
	}
	for i, w := range want {
		im := sum.Impacts[i]
		// The simulation must agree with what the live check decides for the same input.
		live := p.Decide(Request{Server: "claude-code", Tool: im.Tool, Args: im.Args, Arguments: []byte(im.Args)})
		if im.Decision != w || live.Action != w {
			t.Errorf("%s %s: simulated %s, live %s, want %s", im.Tool, im.Args, im.Decision, live.Action, w)
		}
	}
	if sum.Impacts[0].RecordedStatus != event.StatusOK {
		t.Errorf("recorded status lost: %+v", sum.Impacts[0])
	}
}

// Proxied MCP events keep the params envelope, and must keep being read as one.
func TestEvaluateSessionReadsMCPEnvelope(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "read_file", Paths: map[string]string{"path": "/etc/*"}, Action: Deny},
	}}
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "read_file", ID: "1", CallID: "1", Args: `{"name":"read_file","arguments":{"path":"/etc/passwd"}}`},
		// Bare arguments in an MCP session are not what the server would read.
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "read_file", ID: "2", CallID: "2", Args: `{"path":"/etc/passwd"}`},
	}
	sum := EvaluateSession(p, "server", events)
	if sum.Impacts[0].Decision != Deny || sum.Impacts[1].Decision != Allow {
		t.Fatalf("impacts = %+v", sum.Impacts)
	}
}
