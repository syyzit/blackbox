package proxy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/proxy"
)

func TestOversizedFramesStreamThroughWithoutPolicy(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxiedOpts(t, proxy.Options{Sink: sink, MaxFrameBytes: 1000})

	big := strings.Repeat("x", 300_000)
	res, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": big}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), big) {
		t.Fatal("the oversized response was not forwarded intact")
	}
	stop(t, cli, errCh)

	var truncated int
	for _, e := range sink.snapshot() {
		if strings.Contains(e.Error, "recording limit") {
			truncated++
			if len(e.Raw) > 70_000 || e.Size < 300_000 {
				t.Fatalf("oversized frame not recorded as a bounded prefix: raw %d, size %d", len(e.Raw), e.Size)
			}
		}
	}
	if truncated != 2 {
		t.Fatalf("want the request and response recorded as truncated, got %d", truncated)
	}
}

func TestOversizedClientFrameIsBlockedUnderPolicy(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: policy.Default(), Server: "fake-server", ServerKey: "k",
		Ledger: newMapLedger(), MaxFrameBytes: 1000,
	})

	big := strings.Repeat("x", 300_000)
	if err := cli.Send([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"echo","arguments":{"text":"` + big + `"}}}`)); err != nil {
		t.Fatal(err)
	}
	line, err := cli.Next()
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(line, &reply); err != nil || reply.Error == nil || !strings.Contains(reply.Error.Message, "cannot be checked") {
		t.Fatalf("oversized call under policy was not refused: %s", line)
	}

	// The connection keeps working after the dropped frame.
	if _, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "ok"}}); err != nil {
		t.Fatalf("connection broken after an oversized frame: %v", err)
	}
	stop(t, cli, errCh)

	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient && !e.Synthetic && e.Size > 1000 {
			t.Fatal("the server answered the oversized call")
		}
	}
}
