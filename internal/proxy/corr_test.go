package proxy

import (
	"sync"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

type corrSink struct {
	mu     sync.Mutex
	events []event.Event
}

func (s *corrSink) Emit(e event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

type corrLedger struct{ seen map[string]string }

func (l *corrLedger) Observe(server, tool, hash string) bool {
	prev, ok := l.seen[tool]
	l.seen[tool] = hash
	return ok && prev != hash
}

// A response whose id is the string "1" must not consume the pending entry for the
// numeric request 1: if it did, the real tools/list response would arrive with nothing
// to correlate to, and its tool definitions would never reach the rug-pull check.
func TestResponseIDMustMatchRequestIDExactly(t *testing.T) {
	sink := &corrSink{}
	led := &corrLedger{seen: map[string]string{}}
	p := newPipeline(pipelineConfig{SessionID: "s", Sink: sink, Ledger: led, ServerKey: "k"})

	p.record(event.ClientToServer, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), routeHint{}, nil, false)
	p.record(event.ServerToClient, []byte(`{"jsonrpc":"2.0","id":"1","result":{"tools":[]}}`), routeHint{}, nil, false)
	p.record(event.ServerToClient, []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"echo","description":"d","inputSchema":{}}]}}`), routeHint{}, nil, false)

	last := sink.events[len(sink.events)-1]
	if last.Method != "tools/list" || len(last.ToolDefs) != 1 {
		t.Fatalf("real tools/list response was not correlated: method %q, defs %+v", last.Method, last.ToolDefs)
	}
	if _, ok := led.seen["echo"]; !ok {
		t.Fatal("tool definition never reached the ledger")
	}
	if spoof := sink.events[1]; spoof.Method != "" {
		t.Fatalf("string id was matched to the numeric request: %+v", spoof)
	}
}

// Servers re-encode ids: Python's json.dumps escapes non-ASCII, some encoders escape
// "/". The same string spelled differently is the same id; a number is never a string.
func TestResponseIDMatchesByTypeAndValue(t *testing.T) {
	cases := []struct {
		name, req, resp string
		match           bool
	}{
		{"escaped non-ASCII", `"é"`, `"é"`, true},
		{"escaped slash", `"a/b"`, `"a\/b"`, true},
		{"same number", `7`, `7`, true},
		{"number answered as string", `1`, `"1"`, false},
		{"string answered as number", `"1"`, `1`, false},
	}
	for _, tc := range cases {
		for _, scope := range []string{"", "req-1"} {
			sink := &corrSink{}
			p := newPipeline(pipelineConfig{SessionID: "s", Sink: sink})
			hint := routeHint{scope: scope}
			p.record(event.ClientToServer, []byte(`{"jsonrpc":"2.0","id":`+tc.req+`,"method":"tools/list"}`), hint, nil, false)
			p.record(event.ServerToClient, []byte(`{"jsonrpc":"2.0","id":`+tc.resp+`,"result":{"tools":[]}}`), hint, nil, false)
			if got := sink.events[1].Method == "tools/list"; got != tc.match {
				t.Errorf("%s (scope %q): correlated %v, want %v", tc.name, scope, got, tc.match)
			}
			if tc.match && len(p.pending) != 0 {
				t.Errorf("%s (scope %q): pending entry left behind", tc.name, scope)
			}
		}
	}
}
