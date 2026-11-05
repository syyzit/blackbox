package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"github.com/syyzit/blackbox/internal/event"
)

// streamSSE forwards an event stream unchanged and records the JSON payload of each
// event, not the SSE envelope, so streamed frames are queryable like any other frame.
// The hint's fallback name attributes a notification to its subscription.
func (h *HTTP) streamSSE(w http.ResponseWriter, body io.Reader, hint routeHint) {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64*1024)
	var p sseParser
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			if payload, ok := p.line(line); ok {
				h.rec.record(event.ServerToClient, payload, hint, nil, false)
			}
		}
		if err != nil {
			if payload, ok := p.flush(); ok { // a stream that ends without a blank line
				h.rec.record(event.ServerToClient, payload, hint, nil, false)
			}
			return
		}
	}
}

// sseParser turns event-stream lines into event payloads.
//
// Per the SSE format an event ends at a blank line and may span several data: lines,
// which are joined with newlines; event:, id:, retry: and comment lines carry no
// payload.
type sseParser struct {
	data [][]byte
}

// line consumes one line, with or without its terminator, and returns the payload
// of the event it completes, if any.
func (p *sseParser) line(line []byte) ([]byte, bool) {
	field := bytes.TrimRight(line, "\r\n")
	switch {
	case len(field) == 0:
		return p.flush()
	case bytes.HasPrefix(field, []byte("data:")):
		v := bytes.TrimPrefix(field, []byte("data:"))
		v = bytes.TrimPrefix(v, []byte(" ")) // the format strips one leading space
		p.data = append(p.data, append([]byte(nil), v...))
	case bytes.Equal(field, []byte("data")):
		p.data = append(p.data, nil)
	}
	return nil, false
}

// flush ends the current event. "[DONE]" sentinels and empty events are skipped.
func (p *sseParser) flush() ([]byte, bool) {
	if len(p.data) == 0 {
		return nil, false
	}
	payload := bytes.TrimSpace(bytes.Join(p.data, []byte("\n")))
	p.data = p.data[:0]
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}
