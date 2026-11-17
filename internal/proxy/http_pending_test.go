package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

// An exchange that ends without a response to its request must not leave the request
// in the correlation table (nothing can answer it later: the scope is per exchange),
// and the call must be recorded as failed rather than left pending forever.
func TestHTTPFailedExchangeDropsPendingAndRecordsError(t *testing.T) {
	refused := httptest.NewServer(http.NotFoundHandler())
	refusedURL := refused.URL
	refused.Close()

	cases := []struct {
		name     string
		upstream string
		handler  http.HandlerFunc
		// wantErr is part of the recorded error. A response can still arrive on another
		// exchange (a resumed stream), so the text claims only this one.
		wantErr string
	}{
		{name: "connection refused", upstream: refusedURL, wantErr: "upstream: "},
		{name: "non-JSON body", wantErr: "no JSON-RPC response arrived on this HTTP exchange (HTTP 200)", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "upstream exploded")
		}},
		{name: "empty body", wantErr: "on this HTTP exchange (HTTP 500)", handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{name: "stream without a response", wantErr: "on this HTTP exchange (HTTP 200)", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, ": keepalive\n\n")
		}},
		{name: "response to another id", wantErr: "on this HTTP exchange (HTTP 200)", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{}}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := tc.upstream
			if tc.handler != nil {
				up := httptest.NewServer(tc.handler)
				defer up.Close()
				upstream = up.URL
			}
			sink := &corrSink{}
			h, err := NewHTTP(HTTPOptions{Upstream: upstream, SessionID: "s", Sink: sink})
			if err != nil {
				t.Fatal(err)
			}
			front := httptest.NewServer(h)
			defer front.Close()

			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
			resp, err := http.Post(front.URL, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			h.rec.mu.Lock()
			left := len(h.rec.pending)
			h.rec.mu.Unlock()
			if left != 0 {
				t.Fatalf("%d pending entries left after the exchange ended", left)
			}

			sink.mu.Lock()
			defer sink.mu.Unlock()
			var failed *event.Event
			for i, e := range sink.events {
				if e.Direction == event.ServerToClient && e.Status == event.StatusError && e.Tool == "echo" {
					failed = &sink.events[i]
				}
			}
			if failed == nil {
				t.Fatalf("no error recorded for the unanswered call: %+v", sink.events)
			}
			if failed.ID != "1" || failed.CallID != sink.events[0].CallID || !strings.Contains(failed.Error, tc.wantErr) {
				t.Fatalf("error event not tied to the call: %+v", *failed)
			}
		})
	}
}
