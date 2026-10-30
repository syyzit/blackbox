package proxy_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/mcptest"
	"github.com/spacecr8ed/blackbox/internal/proxy"
)

// TestHelperProcess doubles as the MCP server when re-executed by the test binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	_ = mcptest.NewServer().Serve(os.Stdin, os.Stdout)
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

func (m *memSink) snapshot() []event.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]event.Event(nil), m.events...)
}

func startProxied(t *testing.T, sink event.Sink) (*mcptest.Client, chan error) {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		errCh <- proxy.RunStdio(context.Background(), proxy.Options{
			Argv:      []string{os.Args[0], "-test.run=TestHelperProcess"},
			Env:       append(os.Environ(), "GO_WANT_HELPER_PROCESS=1"),
			Stdin:     inR,
			Stdout:    outW,
			Stderr:    os.Stderr,
			SessionID: "test-session",
			Sink:      sink,
		})
	}()

	return mcptest.NewClient(inW, outR), errCh
}

func TestRunStdioForwardsAndRecords(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink)

	res, err := cli.Call("tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if !bytes.Contains(res, []byte("echo")) {
		t.Fatalf("tools/list response missing fake tool: %s", res)
	}

	res, err = cli.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": "hello through the proxy"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !bytes.Contains(res, []byte("hello through the proxy")) {
		t.Fatalf("tool result did not round-trip: %s", res)
	}

	if err := cli.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("proxy returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not exit after stdin closed")
	}

	events := sink.snapshot()
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}

	var sawCall, sawResult bool
	for _, e := range events {
		if e.SessionID != "test-session" {
			t.Fatalf("event missing session id: %+v", e)
		}
		if e.Seq == 0 {
			t.Fatalf("event missing sequence: %+v", e)
		}
		if e.Direction == event.ClientToServer && e.Method == "tools/call" && e.Tool == "echo" {
			sawCall = true
		}
		if e.Direction == event.ServerToClient && e.Method == "" {
			sawResult = true
		}
	}
	if !sawCall {
		t.Fatalf("tools/call for echo was not recorded as a normalized event: %+v", events)
	}
	if !sawResult {
		t.Fatalf("server result frame was not recorded: %+v", events)
	}
}

func TestRecorderPreservesExactBytes(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink)

	const frame = `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`
	if err := cli.Send([]byte(frame)); err != nil {
		t.Fatalf("send: %v", err)
	}
	line, err := cli.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if !bytes.Contains(line, []byte(`"id":7`)) {
		t.Fatalf("unexpected response: %s", line)
	}

	if err := cli.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	<-errCh

	for _, e := range sink.snapshot() {
		if e.Direction != event.ClientToServer || e.Method != "tools/list" {
			continue
		}
		if e.Raw != frame {
			t.Fatalf("recorded frame was rewritten:\n got: %s\nwant: %s", e.Raw, frame)
		}
		return
	}
	t.Fatal("client frame was not recorded")
}
