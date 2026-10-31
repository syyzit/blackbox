package store

import (
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
)

func TestTraceMergesSources(t *testing.T) {
	home := t.TempDir()
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)

	// MCP traffic tagged with the agent session id.
	mcp, err := Open(home, []string{"mcp-server"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "list_directory", Status: event.StatusPending, At: base, ExternalID: "ses_trace", Raw: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "list_directory", Status: event.StatusOK, At: base.Add(time.Second), ExternalID: "ses_trace", Raw: `{}`},
	} {
		if err := mcp.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := mcp.Close(); err != nil {
		t.Fatal(err)
	}

	// Built-in traffic from the plugin, same agent session.
	if err := AppendExternal(home, "ses_trace", []string{"opencode", "builtins"}, []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "shell", Status: event.StatusPending, At: base.Add(2 * time.Second), Raw: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "shell", Status: event.StatusOK, At: base.Add(2*time.Second + 100*time.Millisecond), Raw: `{}`},
	}); err != nil {
		t.Fatal(err)
	}

	tr, err := TraceFor(home, "ses_trace")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 4 {
		t.Fatalf("events = %d", len(tr.Events))
	}
	if len(tr.Sessions) != 2 {
		t.Fatalf("sessions = %+v", tr.Sessions)
	}
	if tr.Events[0].Tool != "list_directory" || tr.Events[3].Tool != "shell" {
		t.Fatalf("events are not in time order: %+v", tr.Events)
	}
	if tr.Events[3].ExternalID != "ses_trace" {
		t.Fatalf("external id missing on built-in event: %+v", tr.Events[3])
	}
	if !tr.StartedAt.Equal(base) {
		t.Fatalf("started = %s", tr.StartedAt)
	}

	list, err := TraceList(home, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ExternalID != "ses_trace" || list[0].Events != 4 || list[0].Sessions != 2 {
		t.Fatalf("trace list = %+v", list)
	}

	// Prefix resolution, the way a user would type an id.
	if _, err := TraceFor(home, "ses_tra"); err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
}
