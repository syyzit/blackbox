package proxy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/mcptest"
	"github.com/spacecr8ed/blackbox/internal/proxy"
	"github.com/spacecr8ed/blackbox/internal/redact"
)

// TestHelperProcess doubles as the MCP server when re-executed by the test binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_LEGACY_SERVER") == "1" {
		fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":99,"method":"roots/list"}`)
		_ = mcptest.NewServer().Serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	srv := mcptest.NewServer()
	if desc := os.Getenv("GO_FAKE_ECHO_DESC"); desc != "" {
		srv.Tools[0].Description = desc
	}
	_ = srv.Serve(os.Stdin, os.Stdout)
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

func startProxied(t *testing.T, sink event.Sink, red *redact.Redactor, extraEnv ...string) (*mcptest.Client, chan error) {
	t.Helper()
	return startProxiedOpts(t, proxy.Options{
		SessionID: "test-session",
		Sink:      sink,
		Redactor:  red,
	}, extraEnv...)
}

func startProxiedOpts(t *testing.T, o proxy.Options, extraEnv ...string) (*mcptest.Client, chan error) {
	t.Helper()
	return startProxiedHandler(t, o, nil, extraEnv...)
}

func startProxiedHandler(t *testing.T, o proxy.Options, handler mcptest.RequestHandler, extraEnv ...string) (*mcptest.Client, chan error) {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errCh := make(chan error, 1)
	env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	env = append(env, extraEnv...)

	o.Argv = []string{os.Args[0], "-test.run=TestHelperProcess"}
	o.Env = env
	o.Stdin = inR
	o.Stdout = outW
	o.Stderr = os.Stderr
	if o.SessionID == "" {
		o.SessionID = "test-session"
	}

	go func() { errCh <- proxy.RunStdio(context.Background(), o) }()

	return mcptest.NewClientWithHandler(inW, outR, handler), errCh
}

func stop(t *testing.T, cli *mcptest.Client, errCh chan error) {
	t.Helper()
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
}

func TestRunStdioForwardsAndRecords(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink, nil)

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

	if _, err := cli.Call("tools/call", map[string]any{
		"name":      "fail",
		"arguments": map[string]any{},
	}); err != nil {
		t.Fatalf("fail tool should return a result with isError, got: %v", err)
	}

	stop(t, cli, errCh)
	events := sink.snapshot()
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}

	var (
		sawCall      bool
		sawResponse  bool
		sawToolError bool
	)
	for _, e := range events {
		if e.SessionID != "test-session" || e.Seq == 0 || e.At.IsZero() {
			t.Fatalf("event missing identity fields: %+v", e)
		}
		if e.Direction == event.ClientToServer && e.Method == "tools/call" && e.Tool == "echo" {
			sawCall = true
			if !strings.Contains(e.Args, "hello through the proxy") {
				t.Fatalf("call args not recorded: %+v", e)
			}
			if e.Status != event.StatusPending {
				t.Fatalf("request status = %q, want pending", e.Status)
			}
		}
		if e.Direction == event.ServerToClient && e.Method == "tools/call" && e.Tool == "echo" {
			sawResponse = true
			if e.DurationMS < 0 || e.Status != event.StatusOK {
				t.Fatalf("bad response metadata: %+v", e)
			}
			if !strings.Contains(e.Result, "hello through the proxy") {
				t.Fatalf("result not recorded: %+v", e)
			}
			if e.ResultType != "complete" {
				t.Fatalf("resultType = %q, want complete", e.ResultType)
			}
		}
		if e.Status == event.StatusToolError {
			sawToolError = true
		}
	}
	if !sawCall || !sawResponse {
		t.Fatalf("missing correlated call/response events: %+v", events)
	}
	if !sawToolError {
		t.Fatalf("tool-level error was not classified: %+v", events)
	}
}

func TestRecorderPreservesExactBytes(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink, nil)

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

	stop(t, cli, errCh)

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

func TestRedactionAppliesOnlyToRecording(t *testing.T) {
	const secret = "sk-livesecret0123456789abcdef"

	sink := &memSink{}
	cli, errCh := startProxied(t, sink, redact.Default())

	res, err := cli.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": secret, "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !bytes.Contains(res, []byte(secret)) {
		t.Fatal("proxy altered the live stream: the agent must still see the real value")
	}

	stop(t, cli, errCh)

	events := sink.snapshot()
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	for _, e := range events {
		blob := e.Raw + e.Args + e.Result + e.Error
		if strings.Contains(blob, secret) {
			t.Fatalf("secret leaked into the recording: %+v", e)
		}
		if strings.Contains(blob, "hunter2hunter2") {
			t.Fatalf("password leaked into the recording: %+v", e)
		}
	}
	if !strings.Contains(events[len(events)-1].Raw, "[REDACTED") {
		t.Fatalf("expected a redaction marker in the recording, got: %s", events[len(events)-1].Raw)
	}
}

func TestToolDefinitionsAreHashed(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink, nil)

	if _, err := cli.Call("tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if _, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "hash me"}}); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	stop(t, cli, errCh)

	for _, e := range sink.snapshot() {
		if len(e.ToolDefs) == 0 {
			continue
		}
		if len(e.ToolDefs) != 4 {
			t.Fatalf("expected 4 tool definitions, got %d: %+v", len(e.ToolDefs), e.ToolDefs)
		}
		for _, d := range e.ToolDefs {
			if len(d.Hash) != 16 {
				t.Fatalf("tool %q has a bad hash: %q", d.Name, d.Hash)
			}
		}

		// The subsequent call must be linked to the definition it ran under.
		for _, call := range sink.snapshot() {
			if call.Direction == event.ClientToServer && call.Tool == "echo" && call.Method == "tools/call" {
				if call.ToolHash == "" {
					t.Fatal("tool call is missing its definition hash")
				}
				return
			}
		}
		t.Fatal("echo call was not recorded")
	}
	t.Fatal("no tool definitions were hashed")
}

func TestCostFromReportedUsage(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink:            sink,
		PriceInPerMTok:  3,
		PriceOutPerMTok: 15,
	})

	if _, err := cli.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": "bill me"},
	}); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	stop(t, cli, errCh)

	// The fake server reports 1200 input and 300 output tokens.
	const want = 1200.0/1e6*3 + 300.0/1e6*15
	for _, e := range sink.snapshot() {
		if e.Direction != event.ServerToClient || e.Tool != "echo" {
			continue
		}
		if e.UsageIn != 1200 || e.UsageOut != 300 {
			t.Fatalf("usage not recorded: %+v", e)
		}
		if math.Abs(e.CostUSD-want) > 1e-9 {
			t.Fatalf("cost = %f, want %f", e.CostUSD, want)
		}
		return
	}
	t.Fatal("echo response not recorded")
}

func TestMRTRRoundsShareOneCallID(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink, nil)

	first, err := cli.Call("tools/call", map[string]any{
		"name":      "confirm",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("first round: %v", err)
	}
	if !strings.Contains(string(first), "input_required") {
		t.Fatalf("expected input_required, got %s", first)
	}

	second, err := cli.Call("tools/call", map[string]any{
		"name":           "confirm",
		"arguments":      map[string]any{},
		"inputResponses": map[string]any{"confirm": map[string]any{"action": "accept"}},
		"requestState":   "state-confirm-1",
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !strings.Contains(string(second), "confirmed") {
		t.Fatalf("expected the final result, got %s", second)
	}

	stop(t, cli, errCh)

	var (
		requests  []event.Event
		responses []event.Event
	)
	for _, e := range sink.snapshot() {
		if e.Tool != "confirm" {
			continue
		}
		if e.Direction == event.ClientToServer {
			requests = append(requests, e)
		} else {
			responses = append(responses, e)
		}
	}
	if len(requests) != 2 || len(responses) != 2 {
		t.Fatalf("expected 2 requests and 2 responses, got %d/%d", len(requests), len(responses))
	}
	if requests[0].CallID == "" || requests[0].CallID != requests[1].CallID {
		t.Fatalf("MRTR rounds were not grouped: %q vs %q", requests[0].CallID, requests[1].CallID)
	}
	if responses[0].Status != event.StatusInputRequired {
		t.Fatalf("first response status = %q", responses[0].Status)
	}
	if responses[1].Status != event.StatusOK {
		t.Fatalf("second response status = %q", responses[1].Status)
	}
	if responses[0].CallID != requests[0].CallID {
		t.Fatalf("response lost the call id: %+v", responses[0])
	}
}

func TestMRTRDoesNotLeakIntoUnrelatedCalls(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxied(t, sink, nil)

	if _, err := cli.Call("tools/call", map[string]any{"name": "confirm", "arguments": map[string]any{}}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "after"}}); err != nil {
		t.Fatalf("echo: %v", err)
	}
	stop(t, cli, errCh)

	ids := map[string]bool{}
	for _, e := range sink.snapshot() {
		if e.Direction == event.ClientToServer && e.Method == "tools/call" {
			ids[e.CallID] = true
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected two distinct logical calls, got %v", ids)
	}
}
