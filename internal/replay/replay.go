// Package replay answers a live agent from a recorded session.
package replay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
	"github.com/spacecr8ed/blackbox/internal/redact"
)

// Exchange is one recorded request paired with the response it received.
type Exchange struct {
	Request  event.Event
	Response event.Event
	Used     bool
}

// Divergence explains how a run differed from the recording.
type Divergence struct {
	Kind   string `json:"kind"` // missing, extra, reordered, args_changed, result_changed
	Index  int64  `json:"index"`
	Method string `json:"method,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Engine replays recorded exchanges.
type Engine struct {
	mu          sync.Mutex
	exchanges   []Exchange
	divergences []Divergence
	last        *Exchange
}

// New pairs requests and responses by JSON-RPC id.
func New(events []event.Event) *Engine {
	e := &Engine{}
	index := map[string]int{}
	for _, ev := range events {
		switch {
		case ev.Direction == event.ClientToServer && ev.Method != "" && ev.Status != event.StatusNotify && ev.ID != "":
			index[ev.ID] = len(e.exchanges)
			e.exchanges = append(e.exchanges, Exchange{Request: ev})
		case ev.Direction == event.ServerToClient && ev.ID != "":
			if i, ok := index[ev.ID]; ok {
				e.exchanges[i].Response = ev
				delete(index, ev.ID)
			}
		}
	}
	return e
}

// Exchanges returns the recorded exchanges in order.
func (e *Engine) Exchanges() []Exchange {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Exchange(nil), e.exchanges...)
}

// Divergences returns what has diverged so far.
func (e *Engine) Divergences() []Divergence {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Divergence(nil), e.divergences...)
}

// LastUsed returns the exchange consumed by the most recent Respond call.
func (e *Engine) LastUsed() *Exchange {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// Respond returns the recorded response for a request frame.
//
// Matching is by method and tool, preferring an exchange whose arguments match
// exactly. When nothing matches, a JSON-RPC error is synthesized and the event is
// recorded as a divergence rather than fabricated.
func (e *Engine) Respond(line []byte) ([]byte, bool) {
	msg, err := jsonrpc.Decode(line)
	if err != nil || !msg.IsRequest() {
		return nil, true
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	best := -1
	for i := range e.exchanges {
		x := &e.exchanges[i]
		if x.Used || x.Response.Raw == "" {
			continue
		}
		if x.Request.Method != msg.Method || x.Request.Tool != msg.ToolName() {
			continue
		}
		if best == -1 {
			best = i
		}
		if canonical(x.Request.Args) == canonical(string(msg.Params)) {
			best = i
			break
		}
	}

	if best == -1 {
		e.divergences = append(e.divergences, Divergence{
			Kind:   "extra",
			Index:  int64(len(e.divergences)),
			Method: msg.Method,
			Tool:   msg.ToolName(),
			Detail: "no recorded response for this call",
		})
		errPayload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": "blackbox replay: no recorded response for " + msg.Method,
			},
		})
		return errPayload, false
	}

	x := &e.exchanges[best]
	x.Used = true
	e.last = x

	if canonical(x.Request.Args) != canonical(string(msg.Params)) {
		e.divergences = append(e.divergences, Divergence{
			Kind:   "args_changed",
			Index:  int64(best),
			Method: msg.Method,
			Tool:   msg.ToolName(),
			Detail: "arguments differ from the recorded call",
		})
	}
	return []byte(x.Response.Raw), true
}

// Options configures ServeStdio.
type Options struct {
	Argv      []string
	Env       []string
	SessionID string
	Sink      event.Sink
	Redactor  *redact.Redactor
	Stderr    io.Writer
}

// ServeStdio runs an agent and answers it from the recording.
//
// The agent is the child process: its stdout carries requests and its stdin receives
// the recorded responses, exactly as if it were talking to the real server.
func ServeStdio(ctx context.Context, eng *Engine, o Options) error {
	if len(o.Argv) == 0 {
		return errors.New("replay: empty command")
	}
	cmd := exec.CommandContext(ctx, o.Argv[0], o.Argv[1:]...)
	cmd.Env = o.Env
	cmd.Stderr = o.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("replay: start %q: %w", o.Argv[0], err)
	}

	rec := &recorder{sessionID: o.SessionID, sink: o.Sink, red: o.Redactor}
	br := bufio.NewReaderSize(stdout, 64*1024)

	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			frame := trim(line)
			rec.add(event.ClientToServer, frame, "", "")

			resp, _ := eng.Respond(frame)
			if len(resp) > 0 {
				if _, err := stdin.Write(append(resp, '\n')); err != nil {
					break
				}
				x := eng.LastUsed()
				if x != nil {
					rec.add(event.ServerToClient, resp, x.Request.Method, x.Request.Tool)
				} else {
					rec.add(event.ServerToClient, resp, "", "")
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	_ = stdin.Close()
	return cmd.Wait()
}

type recorder struct {
	sessionID string
	sink      event.Sink
	red       *redact.Redactor
	seq       int64
}

func (r *recorder) add(dir event.Direction, raw []byte, method, tool string) event.Event {
	r.seq++
	ev := event.Event{
		SessionID: r.sessionID,
		Seq:       r.seq,
		At:        time.Now().UTC(),
		Direction: dir,
		Raw:       r.red.String(string(raw)),
		Size:      len(raw),
	}
	if msg, err := jsonrpc.Decode(raw); err == nil {
		ev.Method = msg.Method
		ev.Tool = msg.ToolName()
		if method != "" {
			ev.Method = method
		}
		if tool != "" {
			ev.Tool = tool
		}
		ev.ID = msg.IDString()
		ev.Args = r.red.String(compact(msg.Params))
		ev.Status = event.StatusPending
		if msg.IsResponse() {
			ev.Status = event.StatusOK
			if msg.Error != nil {
				ev.Status = event.StatusError
			}
			_, isErr := jsonrpc.ParseResult(msg.Result)
			if isErr {
				ev.Status = event.StatusToolError
			}
			ev.Result = r.red.String(compact(msg.Result))
		}
	}
	r.emit(ev)
	return ev
}

func (r *recorder) emit(e event.Event) {
	if r.sink == nil {
		return
	}
	_ = r.sink.Emit(e)
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// canonical normalizes JSON so key order does not count as a difference.
func canonical(s string) string {
	if s == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(b)
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
