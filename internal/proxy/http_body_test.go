package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
)

// A body over the limit used to be cut at the limit and the cut bytes forwarded. A
// proxy that promises byte fidelity refuses it instead, says so in the recording,
// and never lets it reach the server, with or without a policy.
func TestHTTPRefusesOversizedBody(t *testing.T) {
	for _, pol := range []*policy.Policy{nil, policy.Default()} {
		forwarded := false
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		}))
		defer up.Close()

		sink := &corrSink{}
		h, err := NewHTTP(HTTPOptions{Upstream: up.URL, SessionID: "s", Sink: sink, Policy: pol, Server: "srv"})
		if err != nil {
			t.Fatal(err)
		}
		h.maxBody = 1000
		front := httptest.NewServer(h)
		defer front.Close()

		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"` + strings.Repeat("x", 2000) + `"}}}`
		resp, err := http.Post(front.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusRequestEntityTooLarge || forwarded {
			t.Fatalf("policy %v: oversized body got status %d, forwarded %v", pol != nil, resp.StatusCode, forwarded)
		}
		sink.mu.Lock()
		events := append([]event.Event(nil), sink.events...)
		sink.mu.Unlock()
		if len(events) != 1 || events[0].Direction != event.ClientToServer || !strings.Contains(events[0].Error, "refused") {
			t.Fatalf("policy %v: refusal not recorded: %+v", pol != nil, events)
		}
		if pol != nil && events[0].Status != event.StatusDenied {
			t.Fatalf("refusal under a policy not recorded as denied: %+v", events[0])
		}
	}
}
