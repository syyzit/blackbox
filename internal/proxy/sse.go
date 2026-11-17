package proxy

import (
	"bytes"
	"io"
	"net/http"

	"github.com/syyzit/blackbox/internal/event"
)

// streamSSE forwards an event stream unchanged and records the JSON payload of each
// event, not the SSE envelope, so streamed frames are queryable like any other frame.
// The hint's fallback name attributes a notification to its subscription.
//
// Bytes are forwarded as they arrive rather than a line at a time: a line can end in a
// bare \r, and waiting for a \n that never comes would hold the stream back.
func (h *HTTP) streamSSE(w http.ResponseWriter, body io.Reader, hint routeHint) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	var p sseParser
	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
			for _, payload := range p.consume(buf[:n]) {
				h.rec.record(event.ServerToClient, payload, hint, nil, false)
			}
		}
		if err != nil {
			if payload, ok := p.end(); ok { // a stream that ends without a blank line
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
// payload. A line ends at \r\n, \n or \r.
type sseParser struct {
	data    [][]byte
	partial []byte // the current line, not yet terminated
	afterCR bool   // the last line ended in \r, so a \n next completes that terminator
}

// consume takes the next bytes of the stream, split anywhere, and returns the payloads
// of the events they complete.
func (p *sseParser) consume(chunk []byte) [][]byte {
	var out [][]byte
	for len(chunk) > 0 {
		if p.afterCR {
			p.afterCR = false
			if chunk[0] == '\n' {
				chunk = chunk[1:]
				continue
			}
		}
		i := bytes.IndexAny(chunk, "\r\n")
		if i < 0 {
			p.partial = append(p.partial, chunk...)
			return out
		}
		p.partial = append(p.partial, chunk[:i]...)
		p.afterCR = chunk[i] == '\r'
		chunk = chunk[i+1:]
		if payload, ok := p.line(p.partial); ok {
			out = append(out, payload)
		}
		p.partial = p.partial[:0]
	}
	return out
}

// end finishes the stream: an unterminated last line still counts, and so does an
// event that was not followed by a blank line.
func (p *sseParser) end() ([]byte, bool) {
	if len(p.partial) > 0 {
		payload, ok := p.line(p.partial)
		p.partial = p.partial[:0]
		if ok {
			return payload, true
		}
	}
	return p.flush()
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
