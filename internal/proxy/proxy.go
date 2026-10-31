// Package proxy runs an MCP server as a child process and forwards its stdio
// byte-for-byte while recording every frame.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
	"github.com/spacecr8ed/blackbox/internal/policy"
	"github.com/spacecr8ed/blackbox/internal/redact"
)

// Ledger detects tool definitions that changed since they were last seen.
type Ledger interface {
	Observe(server, tool, hash string) bool
}

// Options configures a proxied run.
type Options struct {
	// Argv is the command that starts the MCP server.
	Argv []string
	// Env, when non-nil, replaces the child environment.
	Env []string
	// Stdin is the agent's side of the connection.
	Stdin io.Reader
	// Stdout is where server frames are written back to the agent.
	Stdout io.Writer
	// Stderr is passed through untouched.
	Stderr io.Writer
	// SessionID is stamped onto every recorded event.
	SessionID string
	// Sink receives recorded events. May be nil to disable recording.
	Sink event.Sink
	// Redactor strips secrets from recorded payloads. Nil means no redaction.
	Redactor *redact.Redactor
	// PriceInPerMTok and PriceOutPerMTok convert reported token usage into USD.
	PriceInPerMTok  float64
	PriceOutPerMTok float64

	// Policy gates tool calls. Nil records without enforcing.
	Policy *policy.Policy
	// Server is the human-readable server command used by policy matching.
	Server string
	// ServerKey is the stable fingerprint used by the tool-definition ledger.
	ServerKey string
	// Ledger tracks tool definitions across sessions.
	Ledger Ledger
	// Approver asks a human about require_approval calls.
	Approver policy.Approver
	// Audit persists policy decisions.
	Audit policy.Auditor
}

// RunStdio proxies an MCP stdio server until the child exits.
//
// The proxy is transparent: bytes are forwarded exactly as received, and recording
// happens on a parsed copy. A recording failure never breaks the stream.
func RunStdio(ctx context.Context, o Options) error {
	if len(o.Argv) == 0 {
		return errors.New("proxy: empty command")
	}
	if o.Stdin == nil || o.Stdout == nil {
		return errors.New("proxy: stdin and stdout are required")
	}

	cmd := exec.CommandContext(ctx, o.Argv[0], o.Argv[1:]...)
	cmd.Env = o.Env
	cmd.Stderr = o.Stderr

	childIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdin pipe: %w", err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("proxy: start %q: %w", o.Argv[0], err)
	}

	p := newPipeline(pipelineConfig{
		SessionID: o.SessionID,
		Sink:      o.Sink,
		Redactor:  o.Redactor,
		PriceIn:   o.PriceInPerMTok,
		PriceOut:  o.PriceOutPerMTok,
		AgentOut:  o.Stdout,
		Policy:    o.Policy,
		Server:    o.Server,
		ServerKey: o.ServerKey,
		Ledger:    o.Ledger,
		Approver:  o.Approver,
		Audit:     o.Audit,
	})

	// Agent -> server. Closes the child's stdin on EOF so a piped run terminates.
	c2sErr := make(chan error, 1)
	go func() {
		err := p.pump(childIn, o.Stdin, event.ClientToServer)
		_ = childIn.Close()
		c2sErr <- err
	}()

	// Server -> agent.
	s2cErr := make(chan error, 1)
	go func() {
		s2cErr <- p.pump(o.Stdout, childOut, event.ServerToClient)
	}()

	// Wait for the server's stdout to close before cmd.Wait, per os/exec's contract.
	<-s2cErr
	waitErr := cmd.Wait()

	// The c2s pump can still be blocked reading an interactive terminal. Only collect
	// its result if it is already done; the process is exiting either way.
	select {
	case <-c2sErr:
	case <-time.After(100 * time.Millisecond):
	}

	if waitErr != nil {
		return waitErr
	}
	return nil
}

type call struct {
	method string
	tool   string
	at     time.Time
	callID string
	state  string
}

type openCall struct {
	callID string
	state  string
}

type pipelineConfig struct {
	SessionID string
	Sink      event.Sink
	Redactor  *redact.Redactor
	PriceIn   float64
	PriceOut  float64
	AgentOut  io.Writer
	Policy    *policy.Policy
	Server    string
	ServerKey string
	Ledger    Ledger
	Approver  policy.Approver
	Audit     policy.Auditor
}

type pipeline struct {
	sessionID string
	sink      event.Sink
	red       *redact.Redactor
	seq       atomic.Int64
	priceIn   float64
	priceOut  float64

	agentOut    io.Writer
	writeMu     sync.Mutex
	policy      *policy.Policy
	server      string
	serverKey   string
	ledger      Ledger
	approver    policy.Approver
	audit       policy.Auditor
	rugPulled   map[string]bool
	rugPulledMu sync.Mutex
	toolHashes  map[string]string
	toolHashMu  sync.Mutex

	mu      sync.Mutex
	pending map[string]call

	callMu     sync.Mutex
	openStates map[string]string     // MRTR requestState -> call id
	openKeys   map[string][]openCall // method\x00tool -> open calls
}

func newPipeline(c pipelineConfig) *pipeline {
	return &pipeline{
		sessionID:  c.SessionID,
		sink:       c.Sink,
		red:        c.Redactor,
		priceIn:    c.PriceIn,
		priceOut:   c.PriceOut,
		agentOut:   c.AgentOut,
		policy:     c.Policy,
		server:     c.Server,
		serverKey:  c.ServerKey,
		ledger:     c.Ledger,
		approver:   c.Approver,
		audit:      c.Audit,
		rugPulled:  map[string]bool{},
		toolHashes: map[string]string{},
		pending:    make(map[string]call),
		openStates: make(map[string]string),
		openKeys:   make(map[string][]openCall),
	}
}

func (p *pipeline) pump(dst io.Writer, src io.Reader, dir event.Direction) error {
	br := bufio.NewReaderSize(src, 64*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			if dir == event.ClientToServer {
				msg, dec, blocked := p.gate(line)
				if blocked {
					p.record(dir, line, routeHint{}, dec, false)
					resp := syntheticError(msg, dec.Reason)
					if p.agentOut != nil {
						if err := p.write(p.agentOut, append(resp, '\n')); err != nil {
							return err
						}
					}
					p.record(event.ServerToClient, resp, routeHint{}, dec, true)
				} else {
					// Record before forwarding so a decision that depends on this frame
					// (a rug-pull discovered in tools/list) is visible to the next call.
					p.record(dir, line, routeHint{}, dec, false)
					if err := p.write(dst, line); err != nil {
						return err
					}
				}
			} else {
				p.record(dir, line, routeHint{}, nil, false)
				if err := p.write(dst, line); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// write forwards a frame, serialized so a synthetic block response can never
// interleave with a server response mid-frame.
func (p *pipeline) write(dst io.Writer, line []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := dst.Write(line)
	return err
}

// gate evaluates policy for a client frame. A nil decision means "not evaluated".
func (p *pipeline) gate(line []byte) (*jsonrpc.Message, *policy.Decision, bool) {
	msg, err := jsonrpc.Decode(line)
	if err != nil || msg.Method != "tools/call" || p.policy == nil {
		return msg, nil, false
	}
	dec, blocked := p.evaluate(msg)
	return msg, &dec, blocked
}

func (p *pipeline) evaluate(msg *jsonrpc.Message) (policy.Decision, bool) {
	req := policy.Request{Server: p.server, Tool: msg.ToolName(), Args: string(msg.Params)}
	dec := p.policy.Decide(req)

	if dec.Action == policy.Allow && p.isRugPulled(req.Tool) {
		dec = policy.Decision{
			Action: policy.Deny,
			Rule:   -1,
			Reason: "tool definition changed since it was last approved",
		}
	}

	approved := false
	if dec.Action == policy.RequireApproval {
		if p.approver != nil {
			approved = p.approver.Approve(policy.ApprovalRequest{
				Server: p.server, Tool: req.Tool, Args: req.Args, Reason: dec.Reason,
			})
		}
		if approved {
			dec.Action = policy.Allow
			dec.Reason += " (approved by user)"
		} else {
			dec.Action = policy.Deny
			dec.Reason += " (not approved)"
		}
	}

	if p.audit != nil {
		_ = p.audit.Record(policy.DecisionRecord{
			Server: p.server, Tool: req.Tool, Args: auditArgs(p.red, req.Args),
			Action: dec.Action, Approved: approved, Reason: dec.Reason,
			At: time.Now().UTC(),
		})
	}
	return dec, dec.Action == policy.Deny
}

// auditArgs is the only copy of the arguments that leaves the process for the audit
// log, so it is redacted and bounded even though policy matching used the raw value.
func auditArgs(red *redact.Redactor, args string) string {
	out := red.String(args)
	const max = 4096
	if len(out) > max {
		out = out[:max] + "…"
	}
	return out
}

// callIDFor groups MRTR retries under the id of the initial request. The retry has a
// different JSON-RPC id by spec, so the link is the echoed requestState (preferred) or
// an open call to the same method and tool.
func (p *pipeline) callIDFor(msg *jsonrpc.Message, method, tool, rpcID string) string {
	p.callMu.Lock()
	defer p.callMu.Unlock()

	if state := msg.RequestState(); state != "" {
		if id, ok := p.openStates[state]; ok {
			return id
		}
	}
	if msg.HasInputResponses() {
		key := method + "\x00" + tool
		if open := p.openKeys[key]; len(open) > 0 {
			return open[len(open)-1].callID
		}
	}
	return "call-" + rpcID
}

// trackMRTR keeps the grouping state in sync with the last response of a call.
func (p *pipeline) trackMRTR(c call, resultType string, result json.RawMessage) {
	p.callMu.Lock()
	defer p.callMu.Unlock()

	key := c.method + "\x00" + c.tool
	if resultType != "input_required" {
		p.closeCallLocked(c, key)
		return
	}
	state := jsonrpc.ResultRequestState(result)
	if state != "" {
		p.openStates[state] = c.callID
	}
	p.openKeys[key] = append(p.openKeys[key], openCall{callID: c.callID, state: state})
}

func (p *pipeline) closeCallLocked(c call, key string) {
	for state, id := range p.openStates {
		if id == c.callID {
			delete(p.openStates, state)
		}
	}
	open := p.openKeys[key]
	kept := open[:0]
	for _, oc := range open {
		if oc.callID != c.callID {
			kept = append(kept, oc)
		}
	}
	if len(kept) == 0 {
		delete(p.openKeys, key)
	} else {
		p.openKeys[key] = kept
	}
}

func (p *pipeline) isRugPulled(tool string) bool {
	p.rugPulledMu.Lock()
	defer p.rugPulledMu.Unlock()
	return p.rugPulled[tool]
}

func (p *pipeline) setToolHash(tool, hash string) {
	p.toolHashMu.Lock()
	defer p.toolHashMu.Unlock()
	p.toolHashes[tool] = hash
}

func (p *pipeline) toolHash(tool string) string {
	p.toolHashMu.Lock()
	defer p.toolHashMu.Unlock()
	return p.toolHashes[tool]
}

func (p *pipeline) markRugPulled(tool string) {
	p.rugPulledMu.Lock()
	defer p.rugPulledMu.Unlock()
	p.rugPulled[tool] = true
}

// routeHint carries transport-level routing (Mcp-Method / Mcp-Name headers) that is
// authoritative even when the body cannot be parsed.
type routeHint struct {
	method string
	name   string
}

// record indexes a copy of the frame. Errors are deliberately swallowed: a broken
// recorder must never stall or kill the agent it is observing.
func (p *pipeline) record(dir event.Direction, line []byte, hint routeHint, dec *policy.Decision, synthetic bool) {
	raw := trimEOL(line)
	e := event.Event{
		SessionID: p.sessionID,
		Seq:       p.seq.Add(1),
		At:        time.Now().UTC(),
		Direction: dir,
		Raw:       p.red.String(string(raw)),
		Size:      len(raw),
		Synthetic: synthetic,
	}
	if dec != nil {
		e.Decision = string(dec.Action)
		e.DecisionReason = dec.Reason
		if dec.Action == policy.Deny {
			e.Status = event.StatusDenied
		}
	}

	msg, err := jsonrpc.Decode(raw)
	if err != nil {
		e.Method = hint.method
		e.Tool = hint.name
		p.emit(e) // unparseable frame: preserve the evidence, skip the metadata
		return
	}
	e.SubscriptionID = msg.SubscriptionID()

	method := msg.Method
	if hint.method != "" {
		method = hint.method // header routing is authoritative
	}
	tool := msg.ToolName()
	if tool == "" && hint.name != "" {
		tool = hint.name
	}

	isRequest := msg.IsRequest() || (hint.method != "" && len(msg.ID) > 0)
	isNotification := msg.IsNotification() || (hint.method != "" && len(msg.ID) == 0)

	switch {
	case isRequest:
		e.Method = method
		e.Tool = tool
		e.ID = msg.IDString()
		if e.Status == "" {
			e.Status = event.StatusPending
		}
		e.Args = p.red.String(compact(msg.Params))
		e.CallID = p.callIDFor(msg, e.Method, e.Tool, e.ID)
		if e.Method == "tools/call" && e.Tool != "" {
			e.ToolHash = p.toolHash(e.Tool)
		}

		p.mu.Lock()
		p.pending[e.ID] = call{
			method: e.Method, tool: e.Tool, at: e.At,
			callID: e.CallID, state: msg.RequestState(),
		}
		p.mu.Unlock()

	case isNotification:
		e.Method = method
		e.Tool = tool
		e.Status = event.StatusNotify
		e.Args = p.red.String(compact(msg.Params))

	case msg.IsResponse():
		e.ID = msg.IDString()
		p.mu.Lock()
		c, ok := p.pending[e.ID]
		if ok {
			delete(p.pending, e.ID)
		}
		p.mu.Unlock()
		if ok {
			e.Method = c.method
			e.Tool = c.tool
			e.CallID = c.callID
			e.DurationMS = e.At.Sub(c.at).Milliseconds()
		}

		if msg.Error != nil {
			if e.Status == "" {
				e.Status = event.StatusError
			}
			e.Error = p.red.String(msg.Error.Error())
			break
		}

		resultType, isToolError := jsonrpc.ParseResult(msg.Result)
		e.ResultType = resultType
		e.Result = p.red.String(compact(msg.Result))

		usage := jsonrpc.ParseUsage(msg.Result)
		e.UsageIn, e.UsageOut = usage.InputTokens, usage.OutputTokens
		if p.priceIn != 0 || p.priceOut != 0 {
			e.CostUSD = float64(usage.InputTokens)/1e6*p.priceIn + float64(usage.OutputTokens)/1e6*p.priceOut
		}

		if e.Status == "" {
			switch {
			case isToolError:
				e.Status = event.StatusToolError
			case resultType == "input_required":
				e.Status = event.StatusInputRequired
			default:
				e.Status = event.StatusOK
			}
		}

		if e.CallID != "" {
			p.trackMRTR(c, resultType, msg.Result)
		}

		if e.Method == "tools/list" {
			if defs, err := jsonrpc.ParseTools(msg.Result); err == nil {
				for _, d := range defs {
					hash := HashTool(d)
					changed := false
					if p.ledger != nil {
						changed = p.ledger.Observe(p.serverKey, d.Name, hash)
					}
					if changed && (p.policy == nil || p.policy.RugPullBlock) {
						p.markRugPulled(d.Name)
					}
					e.ToolDefs = append(e.ToolDefs, event.ToolDef{Name: d.Name, Hash: hash, Changed: changed})
					p.setToolHash(d.Name, hash)
				}
			}
		}
	}

	p.emit(e)
}

func (p *pipeline) emit(e event.Event) {
	if p.sink == nil {
		return
	}
	_ = p.sink.Emit(e)
}

// HashTool fingerprints a tool definition so a later run can detect a rug-pull.
//
// The schema is decoded and re-encoded so key order does not affect the hash.
func HashTool(d jsonrpc.ToolDefinition) string {
	var schema any
	_ = json.Unmarshal(d.InputSchema, &schema)
	canonical, _ := json.Marshal(struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      any    `json:"schema"`
	}{d.Name, d.Description, schema})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:16]
}

func syntheticError(msg *jsonrpc.Message, reason string) []byte {
	var id json.RawMessage = []byte("null")
	if msg != nil && len(msg.ID) > 0 {
		id = msg.ID
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    4001,
			"message": "blocked by blackbox policy: " + reason,
		},
	})
	return b
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

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
