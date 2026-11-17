package store

import (
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/event"
)

// recordMCPCall writes a proxied tools/call request and response, untagged, the way
// the proxy records a call from an agent that does not send its session id.
func recordMCPCall(t *testing.T, s *Session, tool, callID, args string, at time.Time) {
	t.Helper()
	for _, e := range []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: tool, CallID: callID,
			Status: event.StatusPending, At: at, Args: args, Raw: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: tool, CallID: callID,
			Status: event.StatusOK, At: at.Add(20 * time.Millisecond), Raw: `{}`},
	} {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinkExternalCallJoinsTheTrace(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)

	mcp, err := Open(home, []string{"fakeserver"})
	if err != nil {
		t.Fatal(err)
	}
	recordMCPCall(t, mcp, "echo", "call-1", `{"name":"echo","arguments":{"text":"first"}}`, now.Add(-3*time.Second))
	recordMCPCall(t, mcp, "echo", "call-2", `{"name":"echo","arguments":{"text":"second"}}`, now.Add(-2*time.Second))
	if err := mcp.Close(); err != nil {
		t.Fatal(err)
	}

	// Key order and spacing differ from the recording; the values are the same.
	linked, err := LinkExternalCall(home, "claude-1", []string{"echo"}, `{ "text": "first" }`,
		now.Add(-time.Minute), now.Add(time.Second))
	if err != nil || !linked {
		t.Fatalf("linked=%v err=%v", linked, err)
	}

	tr, err := TraceFor(home, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 2 {
		t.Fatalf("want the request and its response in the trace, got %d events", len(tr.Events))
	}
	for _, e := range tr.Events {
		if e.CallID != "call-1" {
			t.Fatalf("linked the wrong call: %+v", e)
		}
	}

	// The same call cannot be claimed twice, and with two different calls left a
	// non-matching request must not be guessed.
	linked, err = LinkExternalCall(home, "claude-2", []string{"echo"}, `{"text":"first"}`,
		now.Add(-time.Minute), now.Add(time.Second))
	if err != nil || !linked {
		// Only call-2 is still untagged, so it is the single candidate.
		t.Fatalf("single remaining candidate: linked=%v err=%v", linked, err)
	}
}

func TestLinkExternalCallDoesNotGuessBetweenCandidates(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)

	mcp, err := Open(home, []string{"fakeserver"})
	if err != nil {
		t.Fatal(err)
	}
	recordMCPCall(t, mcp, "echo", "call-1", `{"name":"echo","arguments":{"text":"a"}}`, now.Add(-3*time.Second))
	recordMCPCall(t, mcp, "echo", "call-2", `{"name":"echo","arguments":{"text":"b"}}`, now.Add(-2*time.Second))
	recordMCPCall(t, mcp, "read", "call-3", `{"name":"read","arguments":{}}`, now.Add(-time.Second))
	if err := mcp.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, tool, args string
		since            time.Time
		want             bool
	}{
		{"no equal arguments, two candidates", "echo", `{"text":"c"}`, now.Add(-time.Minute), false},
		{"another tool", "write", `{}`, now.Add(-time.Minute), false},
		{"outside the window", "echo", `{"text":"a"}`, now.Add(-500 * time.Millisecond), false},
		{"missing arguments equal an empty object", "read", ``, now.Add(-time.Minute), true},
	}
	for _, c := range cases {
		linked, err := LinkExternalCall(home, "claude-1", []string{c.tool}, c.args, c.since, now.Add(time.Second))
		if err != nil || linked != c.want {
			t.Errorf("%s: linked=%v err=%v, want %v", c.name, linked, err, c.want)
		}
	}
}
