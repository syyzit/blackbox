package proxy

import (
	"bytes"
	"fmt"
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
	p := sseParser{max: h.rec.maxFrame}
	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
			h.recordSSE(p.consume(buf[:n]), hint)
		}
		if err != nil {
			h.recordSSE(p.end(), hint) // a stream that ends without a blank line
			return
		}
	}
}

func (h *HTTP) recordSSE(items []sseItem, hint routeHint) {
	for _, it := range items {
		if it.payload != nil {
			h.rec.record(event.ServerToClient, it.payload, hint, nil, false)
			continue
		}
		note := fmt.Sprintf("SSE line of %d bytes exceeded the %d byte recording limit; its event was not recorded", it.size, h.rec.maxFrame)
		h.rec.recordOversize(event.ServerToClient, it.head, it.size, note, nil)
	}
}

// sseItem is what a stream yields to the recorder: an event's payload, or, when
// payload is nil, a line over the limit, with its size and the part that was kept.
type sseItem struct {
	payload []byte
	head    []byte
	size    int
}

// sseParser turns event-stream lines into event payloads.
//
// Per the SSE format an event ends at a blank line and may span several data: lines,
// which are joined with newlines; event:, id:, retry: and comment lines carry no
// payload. A line ends at \r\n, \n or \r.
//
// A line longer than max (when max is set) is not held in memory: only its first max
// bytes are kept, and when it ends, the event it belongs to is dropped and reported
// instead. Half an event recorded as if it were the whole one would mislead.
type sseParser struct {
	max       int
	data      [][]byte
	partial   []byte // the current line, not yet terminated, up to max bytes of it
	lineLen   int    // the current line's full length so far
	afterCR   bool   // the last line ended in \r, so a \n next completes that terminator
	dropEvent bool   // a line of the current event was over max
}

// consume takes the next bytes of the stream, split anywhere, and returns what the
// lines they complete yield.
func (p *sseParser) consume(chunk []byte) []sseItem {
	var out []sseItem
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
			p.keep(chunk)
			return out
		}
		p.keep(chunk[:i])
		p.afterCR = chunk[i] == '\r'
		chunk = chunk[i+1:]
		out = p.endLine(out)
	}
	return out
}

// keep adds bytes to the current line, as far as the limit allows.
func (p *sseParser) keep(b []byte) {
	p.lineLen += len(b)
	if p.max > 0 {
		if room := p.max - len(p.partial); room < len(b) {
			b = b[:max(room, 0)]
		}
	}
	p.partial = append(p.partial, b...)
}

// endLine handles the line that just ended.
func (p *sseParser) endLine(out []sseItem) []sseItem {
	line, size := p.partial, p.lineLen
	p.partial, p.lineLen = p.partial[:0], 0
	switch {
	case p.max > 0 && size > p.max:
		p.data = p.data[:0]
		p.dropEvent = true
		return append(out, sseItem{head: append([]byte(nil), line...), size: size})
	case p.dropEvent:
		// The rest of a dropped event: skipped up to the blank line that ends it.
		if len(line) == 0 {
			p.dropEvent = false
		}
		return out
	}
	if payload, ok := p.line(line); ok {
		out = append(out, sseItem{payload: payload})
	}
	return out
}

// end finishes the stream: an unterminated last line still counts, and so does an
// event that was not followed by a blank line.
func (p *sseParser) end() []sseItem {
	var out []sseItem
	if p.lineLen > 0 {
		out = p.endLine(out)
	}
	if !p.dropEvent {
		if payload, ok := p.flush(); ok {
			out = append(out, sseItem{payload: payload})
		}
	}
	p.dropEvent = false
	return out
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
