package policy

import (
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

func impactEvents() []event.Event {
	return []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "read_file", ID: "1", CallID: "call-1", Status: event.StatusPending, Args: `{"path":"/tmp/a"}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "read_file", ID: "1", CallID: "call-1", Status: event.StatusOK},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "write_file", ID: "2", CallID: "call-2", Status: event.StatusPending, Args: `{"path":"/tmp/b"}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "write_file", ID: "2", CallID: "call-2", Status: event.StatusOK},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "write_file", ID: "3", CallID: "call-3", Status: event.StatusPending, Args: `{"path":"/etc/hosts"}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "write_file", ID: "3", CallID: "call-3", Status: event.StatusToolError},
	}
}

func TestEvaluateSessionCountsDecisions(t *testing.T) {
	p := &Policy{Default: Allow, Rules: []Rule{
		{Tool: "write_file", Action: Deny, Reason: "no writes"},
	}}
	sum := EvaluateSession(p, "server", impactEvents())

	if sum.Total != 3 || sum.Deny != 2 || sum.Allow != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(sum.Impacts) != 3 {
		t.Fatalf("impacts = %d", len(sum.Impacts))
	}
	if sum.Impacts[1].Tool != "write_file" || sum.Impacts[1].RecordedStatus != event.StatusOK {
		t.Fatalf("bad impact: %+v", sum.Impacts[1])
	}
	if sum.Impacts[2].RecordedStatus != event.StatusToolError {
		t.Fatalf("bad status: %+v", sum.Impacts[2])
	}
}

func TestEvaluateSessionCollapsesMRTRRounds(t *testing.T) {
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "confirm", ID: "1", CallID: "call-1", Status: event.StatusPending, Args: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "confirm", ID: "1", CallID: "call-1", Status: event.StatusInputRequired},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "confirm", ID: "2", CallID: "call-1", Status: event.StatusPending, Args: `{"inputResponses":{}}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "confirm", ID: "2", CallID: "call-1", Status: event.StatusOK},
	}
	sum := EvaluateSession(&Policy{Default: Allow}, "server", events)
	if sum.Total != 1 {
		t.Fatalf("MRTR rounds were counted separately: %+v", sum)
	}
	if sum.Impacts[0].RecordedStatus != event.StatusOK {
		t.Fatalf("recorded status should be the final round: %+v", sum.Impacts[0])
	}
}
