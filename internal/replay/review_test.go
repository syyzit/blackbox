package replay

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

func kinds(r Report) []string {
	out := []string{}
	for _, c := range r.Changes {
		out = append(out, c.Kind)
	}
	return out
}

// A call that one run makes more often than the other is a difference, even though
// the same call exists in both.
func TestDiffCountsRepeatedCalls(t *testing.T) {
	del := `{"name":"delete_file","arguments":{"path":"x"}}`
	other := `{"name":"delete_file","arguments":{"path":"y"}}`
	once := []event.Event{
		request("1", "tools/call", "delete_file", del), response("1", `{"ok":true}`),
	}
	twice := []event.Event{
		request("1", "tools/call", "delete_file", del), response("1", `{"ok":true}`),
		request("2", "tools/call", "delete_file", del), response("2", `{"ok":true}`),
	}
	// Two of one call and one of another, against one and two: after the common
	// calls are matched, one x in A pairs with nothing and one y in B likewise.
	mixedA := []event.Event{
		request("1", "tools/call", "delete_file", del),
		request("2", "tools/call", "delete_file", other),
		request("3", "tools/call", "delete_file", del),
	}
	mixedB := []event.Event{
		request("1", "tools/call", "delete_file", other),
		request("2", "tools/call", "delete_file", del),
		request("3", "tools/call", "delete_file", other),
	}

	cases := []struct {
		name string
		a, b []event.Event
		want []string
	}{
		{"repeated in B", once, twice, []string{"extra"}},
		{"repeated in A", twice, once, []string{"missing"}},
		{"same repeats", twice, twice, []string{}},
		{"different repeats", mixedA, mixedB, []string{"missing", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := Diff(tc.a, tc.b)
			got := kinds(rep)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("changes = %v, want %v (%+v)", got, tc.want, rep)
			}
			if rep.Identical != (len(tc.want) == 0) {
				t.Fatalf("identical = %v with changes %v", rep.Identical, got)
			}
		})
	}
}

// A merged trace holds several MCP sessions, and each numbers its requests from 1.
// Calls from different sessions with the same id are different calls.
func TestDiffKeepsCallsOfDifferentSessionsApart(t *testing.T) {
	in := func(session string, e event.Event) event.Event {
		e.SessionID = session
		if e.Direction == event.ClientToServer {
			e.CallID = "call-" + e.ID
		}
		return e
	}
	trace := []event.Event{
		in("fs", request("1", "tools/call", "read", `{"name":"read","arguments":{"path":"a"}}`)),
		in("fs", response("1", `{"text":"from fs"}`)),
		in("shell", request("1", "tools/call", "run", `{"name":"run","arguments":{"cmd":"ls"}}`)),
		in("shell", response("1", `{"text":"from shell"}`)),
	}

	rep := Diff(trace, trace)
	if rep.CallsA != 2 || rep.CallsB != 2 || !rep.Identical {
		t.Fatalf("two sessions' calls were merged: %+v", rep)
	}

	// The first session's result must be compared with itself, not with the
	// response the other session gave to its own request 1.
	changed := append([]event.Event(nil), trace...)
	changed[1] = in("fs", response("1", `{"text":"something else"}`))
	rep = Diff(trace, changed)
	if got := kinds(rep); len(got) != 1 || got[0] != "result_changed" || rep.Changes[0].Tool != "read" {
		t.Fatalf("changes = %+v", rep.Changes)
	}

	// Without the second session's call, run B is missing it.
	rep = Diff(trace, trace[:2])
	if got := kinds(rep); len(got) != 1 || got[0] != "missing" || rep.Changes[0].Tool != "run" {
		t.Fatalf("changes = %+v", rep.Changes)
	}
}

func TestEngineForgetsLastExchangeWhenNothingMatches(t *testing.T) {
	eng := New([]event.Event{
		request("1", "tools/call", "echo", `{"name":"echo","arguments":{}}`),
		response("1", `{"jsonrpc":"2.0","id":1,"result":{}}`),
	})

	if _, ok := eng.Respond([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)); !ok {
		t.Fatal("recorded call not answered")
	}
	if eng.LastUsed() == nil {
		t.Fatal("no exchange reported for an answered call")
	}
	if _, ok := eng.Respond([]byte(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"other","arguments":{}}}`)); ok {
		t.Fatal("unrecorded call was answered")
	}
	if x := eng.LastUsed(); x != nil {
		t.Fatalf("unmatched request still reports the previous exchange: %+v", x.Request)
	}

	// A notification is not part of the previous call either.
	eng = New([]event.Event{
		request("1", "tools/call", "echo", `{"name":"echo","arguments":{}}`),
		response("1", `{"jsonrpc":"2.0","id":1,"result":{}}`),
	})
	eng.Respond([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	eng.Respond([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled"}`))
	if x := eng.LastUsed(); x != nil {
		t.Fatalf("a notification reports the previous exchange: %+v", x.Request)
	}
}

// TestReplayUnmatchedAgentHelper acts as an agent that makes a recorded call and then
// one the recording does not have.
func TestReplayUnmatchedAgentHelper(t *testing.T) {
	if os.Getenv("GO_REPLAY_UNMATCHED_AGENT") != "1" {
		return
	}
	br := bufio.NewReader(os.Stdin)
	for _, req := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"file:///x"}}`,
	} {
		fmt.Fprintln(os.Stdout, req)
		if _, err := br.ReadString('\n'); err != nil {
			fmt.Fprintln(os.Stderr, "agent read:", err)
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestServeStdioRecordsUnmatchedCallUnderItsOwnName(t *testing.T) {
	recorded := request("1", "tools/call", "echo", `{"name":"echo","arguments":{}}`)
	recorded.CallID = "call-1"
	eng := New([]event.Event{recorded, response("1", `{"jsonrpc":"2.0","id":1,"result":{}}`)})

	sink := &memSink{}
	err := ServeStdio(context.Background(), eng, Options{
		Argv:      []string{os.Args[0], "-test.run=TestReplayUnmatchedAgentHelper"},
		Env:       append(os.Environ(), "GO_REPLAY_UNMATCHED_AGENT=1"),
		SessionID: "replay-test",
		Sink:      sink,
		Stderr:    os.Stderr,
	})
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if len(sink.events) != 4 {
		t.Fatalf("recorded %d frames, want 4: %+v", len(sink.events), sink.events)
	}
	if got := sink.events[1]; got.Tool != "echo" || got.CallID != "call-1" {
		t.Fatalf("matched response = %+v", got)
	}
	for _, got := range sink.events[2:] {
		if got.Method != "resources/read" || got.Tool != "" || got.CallID != "" {
			t.Fatalf("unmatched %s frame recorded as method %q tool %q call %q",
				got.Direction, got.Method, got.Tool, got.CallID)
		}
	}
	if sink.events[3].Status != event.StatusError {
		t.Fatalf("synthesized error status = %q", sink.events[3].Status)
	}
}
