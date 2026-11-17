package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

// The SSE format ends a line with \n, \r or \r\n. A stream that uses any of them is
// forwarded unchanged and recorded the same way.
func TestSSELineEndings(t *testing.T) {
	events := []string{
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`,
	}
	for name, eol := range map[string]string{"LF": "\n", "CRLF": "\r\n", "CR": "\r"} {
		t.Run(name, func(t *testing.T) {
			stream := ": keepalive" + eol + eol
			for _, ev := range events {
				stream += "event: message" + eol + "data: " + ev + eol + eol
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, stream)
			}))
			defer up.Close()
			sink := &corrSink{}
			h, err := NewHTTP(HTTPOptions{Upstream: up.URL, SessionID: "s", Sink: sink})
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
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(got) != stream {
				t.Fatalf("stream was altered: %q", got)
			}

			sink.mu.Lock()
			defer sink.mu.Unlock()
			var recorded []string
			for _, e := range sink.events {
				if e.Direction == event.ServerToClient && !e.Synthetic {
					recorded = append(recorded, e.Raw)
				}
			}
			if strings.Join(recorded, "|") != strings.Join(events, "|") {
				t.Fatalf("recorded %q, want %q", recorded, events)
			}
		})
	}
}

// A \r\n pair can be split across two reads; the \n must not end a second line, which
// would read as a blank line and end the event early.
func TestSSEParserCRLFSplitAcrossReads(t *testing.T) {
	stream := []byte("data: {\"a\":\r\ndata: 1}\r\n\r\n")
	for cut := 0; cut <= len(stream); cut++ {
		var p sseParser
		got := payloads(append(append(p.consume(stream[:cut]), p.consume(stream[cut:])...), p.end()...))
		if len(got) != 1 || !bytes.Equal(got[0], []byte("{\"a\":\n1}")) {
			t.Fatalf("cut at %d: got %q", cut, got)
		}
	}
}
