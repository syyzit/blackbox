package replay

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
)

func ev(dir event.Direction, method, tool, args, result string, seq int64) event.Event {
	return event.Event{
		SessionID: "s", Seq: seq, At: time.Unix(0, 0), Direction: dir,
		Method: method, Tool: tool, Args: args, Result: result, Raw: "{}",
	}
}

func request(id, method, tool, args string) event.Event {
	e := ev(event.ClientToServer, method, tool, args, "", 0)
	e.ID = id
	e.Status = event.StatusPending
	return e
}

func response(id, result string) event.Event {
	e := ev(event.ServerToClient, "", "", "", result, 0)
	e.Raw = result
	e.ID = id
	e.Status = event.StatusOK
	e.Method = "tools/call"
	return e
}

func TestDiffIdentical(t *testing.T) {
	a := []event.Event{
		request("1", "tools/list", "", `{}`),
		response("1", `{"tools":[]}`),
		request("2", "tools/call", "echo", `{"name":"echo","arguments":{"text":"hi"}}`),
		response("2", `{"content":[{"text":"hi"}]}`),
	}
	report := Diff(a, a)
	if !report.Identical {
		t.Fatalf("expected identical, got %+v", report)
	}
	if report.CallsA != 2 || report.CallsB != 2 {
		t.Fatalf("wrong call counts: %+v", report)
	}
}

func TestDiffDetectsExtraAndMissingCalls(t *testing.T) {
	a := []event.Event{
		request("1", "tools/list", "", `{}`),
		request("2", "tools/call", "echo", `{"name":"echo","arguments":{"text":"hi"}}`),
	}
	b := []event.Event{
		request("1", "tools/list", "", `{}`),
		request("3", "tools/call", "add", `{"a":1,"b":2}`),
	}
	report := Diff(a, b)
	if report.Identical {
		t.Fatal("expected divergence")
	}
	kinds := map[string]int{}
	for _, c := range report.Changes {
		kinds[c.Kind]++
	}
	if kinds["missing"] != 1 || kinds["extra"] != 1 {
		t.Fatalf("expected one missing and one extra, got %+v", report.Changes)
	}
}

func TestDiffDetectsReorderedCalls(t *testing.T) {
	a := []event.Event{
		request("1", "tools/call", "echo", `{"text":"a"}`),
		request("2", "tools/call", "add", `{"a":1,"b":2}`),
	}
	b := []event.Event{
		request("1", "tools/call", "add", `{"a":1,"b":2}`),
		request("2", "tools/call", "echo", `{"text":"a"}`),
	}
	report := Diff(a, b)
	seen := false
	for _, c := range report.Changes {
		if c.Kind == "reordered" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("expected a reordered change, got %+v", report.Changes)
	}
}

func TestDiffDetectsChangedResult(t *testing.T) {
	a := []event.Event{request("1", "tools/call", "echo", `{"text":"a"}`), response("1", `{"text":"a"}`)}
	b := []event.Event{request("1", "tools/call", "echo", `{"text":"a"}`), response("1", `{"text":"b"}`)}
	report := Diff(a, b)
	if len(report.Changes) != 1 || report.Changes[0].Kind != "result_changed" {
		t.Fatalf("expected result_changed, got %+v", report.Changes)
	}
}

func TestEngineReplaysByMethodAndTool(t *testing.T) {
	events := []event.Event{
		request("1", "tools/list", "", `{}`),
		response("1", `{"tools":[{"name":"echo"}]}`),
		request("2", "tools/call", "echo", `{"name":"echo","arguments":{"text":"hi"}}`),
		func() event.Event { e := response("2", `{"content":[{"text":"hi"}]}`); return e }(),
	}
	eng := New(events)
	if got := len(eng.Exchanges()); got != 2 {
		t.Fatalf("expected 2 exchanges, got %d", got)
	}

	resp, matched := eng.Respond([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`))
	if !matched {
		t.Fatal("expected a match")
	}
	if string(resp) != `{"content":[{"text":"hi"}]}` {
		t.Fatalf("wrong response: %s", resp)
	}
	if len(eng.Divergences()) != 0 {
		t.Fatalf("unexpected divergence: %+v", eng.Divergences())
	}

	// Second identical call has no recorded counterpart left.
	resp, matched = eng.Respond([]byte(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`))
	if matched {
		t.Fatal("expected a miss")
	}
	var msg jsonrpc.Message
	if err := json.Unmarshal(resp, &msg); err != nil {
		t.Fatalf("synthetic response is not JSON-RPC: %s", resp)
	}
	if msg.Error == nil || msg.Error.Code != -32601 {
		t.Fatalf("expected -32601, got %s", resp)
	}
	if len(eng.Divergences()) != 1 || eng.Divergences()[0].Kind != "extra" {
		t.Fatalf("expected an extra divergence, got %+v", eng.Divergences())
	}
}

func TestEngineFlagsChangedArguments(t *testing.T) {
	events := []event.Event{
		request("1", "tools/call", "echo", `{"name":"echo","arguments":{"text":"original"}}`),
		response("1", `{"ok":true}`),
	}
	eng := New(events)
	resp, matched := eng.Respond([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"changed"}}}`))
	if !matched {
		t.Fatal("should still match by tool")
	}
	if len(resp) == 0 {
		t.Fatal("expected the recorded response")
	}
	divs := eng.Divergences()
	if len(divs) != 1 || divs[0].Kind != "args_changed" {
		t.Fatalf("expected args_changed, got %+v", divs)
	}
}
