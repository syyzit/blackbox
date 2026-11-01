package proxy

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
)

// DefaultMaxFrame bounds how much of one stdio frame is held in memory. MCP frames
// are single JSON lines; one larger than this is still forwarded (or blocked), but
// it is streamed through and only its beginning is recorded.
const DefaultMaxFrame = 64 << 20

// recordedPrefix is how much of an oversized frame is kept as evidence.
const recordedPrefix = 64 << 10

// readFrame reads one newline-terminated frame. When the frame grows past max, it
// returns what it has with oversize set; the rest is still unread.
func readFrame(br *bufio.Reader, max int) (frame []byte, oversize bool, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		frame = append(frame, chunk...)
		if err == bufio.ErrBufferFull {
			if len(frame) > max {
				return frame, true, nil
			}
			continue
		}
		return frame, false, err
	}
}

// oversize handles a frame larger than the limit. head is what was read so far.
//
// A client frame that policy should see cannot be parsed without holding it whole, so
// with a policy it is dropped and the agent gets an error: forwarding it unchecked
// would reopen the gate. Everything else is streamed through unchanged, holding the
// write lock so a synthetic reply cannot land in the middle of it.
func (p *pipeline) oversize(dst io.Writer, br *bufio.Reader, dir event.Direction, head []byte) error {
	total := len(head)

	if dir == event.ClientToServer && p.policy != nil {
		n, readErr := skipLine(br)
		total += n
		reason := fmt.Sprintf("frame of %d bytes exceeds the %d byte limit and cannot be checked", total, p.maxFrame)
		dec := &policy.Decision{Action: policy.Deny, Mode: policy.ModeEnforce, Rule: -1, Reason: reason}
		p.recordOversize(dir, head, total, dec)
		if p.agentOut != nil {
			resp := syntheticError(nil, reason)
			if err := p.write(p.agentOut, append(resp, '\n')); err != nil {
				return err
			}
			p.record(event.ServerToClient, resp, routeHint{}, dec, true)
		}
		return readErr
	}

	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := dst.Write(head); err != nil {
		return err
	}
	var readErr error
	for {
		chunk, err := br.ReadSlice('\n')
		total += len(chunk)
		if len(chunk) > 0 {
			if _, werr := dst.Write(chunk); werr != nil {
				return werr
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		readErr = err
		break
	}
	p.recordOversize(dir, head, total, nil)
	return readErr
}

// skipLine discards input up to and including the next newline.
func skipLine(br *bufio.Reader) (int, error) {
	n := 0
	for {
		chunk, err := br.ReadSlice('\n')
		n += len(chunk)
		if err != bufio.ErrBufferFull {
			return n, err
		}
	}
}

// recordOversize keeps the beginning of an oversized frame as evidence. It cannot be
// parsed, so it carries no method or tool.
func (p *pipeline) recordOversize(dir event.Direction, head []byte, total int, dec *policy.Decision) {
	prefix := head
	if len(prefix) > recordedPrefix {
		prefix = prefix[:recordedPrefix]
	}
	e := event.Event{
		SessionID: p.sessionID,
		Seq:       p.seq.Add(1),
		At:        time.Now().UTC(),
		Direction: dir,
		Raw:       p.red.String(string(prefix)) + "…",
		Size:      total,
		Error:     fmt.Sprintf("frame of %d bytes exceeded the %d byte recording limit; only the first %d bytes are kept", total, p.maxFrame, len(prefix)),
	}
	if dec != nil {
		e.Decision = string(dec.Action)
		e.DecisionReason = dec.Reason
		e.Status = event.StatusDenied
	}
	p.emit(e)
}
