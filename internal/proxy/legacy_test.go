package proxy_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/proxy"
)

// Legacy MCP servers can ask the client for roots/sampling/elicitation. The recorder
// must correlate the client's reply to that request without touching the table used for
// client-initiated calls.
func TestServerInitiatedRequestIsCorrelated(t *testing.T) {
	answered := make(chan string, 1)
	sink := &memSink{}

	cli, errCh := startProxiedHandler(t, proxy.Options{Sink: sink}, func(method string, params json.RawMessage) (any, error) {
		answered <- method
		return map[string]any{"roots": []any{}}, nil
	}, "GO_LEGACY_SERVER=1")

	select {
	case method := <-answered:
		if method != "roots/list" {
			t.Fatalf("answered unexpected request %q", method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server-initiated request never reached the client")
	}

	// The client-initiated traffic must still work after the server request.
	if _, err := cli.Call("tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	stop(t, cli, errCh)

	var request, reply bool
	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient && e.Method == "roots/list" && e.ID == "99" {
			request = true
			if e.Status != event.StatusPending {
				t.Fatalf("server request status = %q", e.Status)
			}
		}
		if e.Direction == event.ClientToServer && e.Method == "roots/list" && e.ID == "99" {
			reply = true
			if e.Status != event.StatusOK {
				t.Fatalf("client reply status = %q", e.Status)
			}
		}
	}
	if !request || !reply {
		t.Fatalf("server request was not paired with its reply: %+v", sink.snapshot())
	}
}
