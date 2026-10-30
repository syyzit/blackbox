package replay

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
)

// TestReplayAgentHelper acts as the agent when re-executed by ServeStdio.
func TestReplayAgentHelper(t *testing.T) {
	if os.Getenv("GO_REPLAY_AGENT") != "1" {
		return
	}
	br := bufio.NewReader(os.Stdin)

	ask := func(req, want string) {
		fmt.Fprintln(os.Stdout, req)
		line, err := br.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent read:", err)
			os.Exit(3)
		}
		if !strings.Contains(line, want) {
			fmt.Fprintf(os.Stderr, "agent got %q, want %q\n", line, want)
			os.Exit(4)
		}
	}

	ask(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, `"tools"`)
	ask(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"replayed"}}}`, "replayed")
	os.Exit(0)
}

type memSink struct {
	mu     sync.Mutex
	events []event.Event
}

func (m *memSink) Emit(e event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func TestServeStdioReplaysRecordedSession(t *testing.T) {
	events := []event.Event{
		{
			Direction: event.ClientToServer, Method: "tools/list", ID: "1",
			Status: event.StatusPending, Args: `{}`, Raw: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		},
		{
			Direction: event.ServerToClient, ID: "1", Method: "tools/list", Status: event.StatusOK,
			Result: `{"resultType":"complete","tools":[]}`,
			Raw:    `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","tools":[]}}`,
		},
		{
			Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", ID: "2",
			Status: event.StatusPending, Args: `{"name":"echo","arguments":{"text":"replayed"}}`,
			Raw: `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"replayed"}}}`,
		},
		{
			Direction: event.ServerToClient, ID: "2", Method: "tools/call", Status: event.StatusOK,
			Result: `{"resultType":"complete","content":[{"type":"text","text":"replayed"}]}`,
			Raw:    `{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete","content":[{"type":"text","text":"replayed"}]}}`,
		},
	}

	eng := New(events)
	sink := &memSink{}
	err := ServeStdio(context.Background(), eng, Options{
		Argv:      []string{os.Args[0], "-test.run=TestReplayAgentHelper"},
		Env:       append(os.Environ(), "GO_REPLAY_AGENT=1"),
		SessionID: "replay-test",
		Sink:      sink,
		Stderr:    os.Stderr,
	})
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if divs := eng.Divergences(); len(divs) != 0 {
		t.Fatalf("unexpected divergences: %+v", divs)
	}
	if len(sink.events) != 4 {
		t.Fatalf("expected 4 recorded frames, got %d", len(sink.events))
	}
	if sink.events[1].Raw == "" || sink.events[3].Raw == "" {
		t.Fatalf("responses were not recorded: %+v", sink.events)
	}
}
