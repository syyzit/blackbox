// Package policy decides whether a tool call may proceed.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Action is what the gate should do with a call.
type Action string

const (
	Allow           Action = "allow"
	Deny            Action = "deny"
	RequireApproval Action = "require_approval"
)

// Mode controls whether decisions are enforced or only recorded.
type Mode string

const (
	// ModeEnforce blocks calls the policy denies.
	ModeEnforce Mode = "enforce"
	// ModeShadow records what would have happened and lets the call through.
	ModeShadow Mode = "shadow"
)

// Rule matches tool calls and returns an action. Empty fields match anything.
type Rule struct {
	Server string `json:"server,omitempty"` // wildcard over the server command
	Tool   string `json:"tool,omitempty"`   // wildcard over the tool name
	Args   string `json:"args,omitempty"`   // regex over the raw arguments
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`

	argsRe   *regexp.Regexp
	serverRe *regexp.Regexp
	toolRe   *regexp.Regexp
}

// Policy is the on-disk policy file.
type Policy struct {
	Default      Action `json:"default"`
	Mode         Mode   `json:"mode"`
	RugPullBlock bool   `json:"rugPullBlock"`
	Rules        []Rule `json:"rules"`
}

// Default returns a permissive policy that still blocks changed tool definitions.
func Default() *Policy {
	return &Policy{Default: Allow, Mode: ModeEnforce, RugPullBlock: true}
}

// Load reads a policy file. A missing path yields the default policy.
func Load(path string) (*Policy, error) {
	p := Default()
	if path == "" {
		return p, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	if p.Default == "" {
		p.Default = Allow
	}
	if p.Mode == "" {
		p.Mode = ModeEnforce
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Policy) validate() error {
	if p.Mode == "" {
		p.Mode = ModeEnforce
	}
	switch p.Mode {
	case ModeEnforce, ModeShadow:
	default:
		return fmt.Errorf("policy: unknown mode %q (use enforce or shadow)", p.Mode)
	}
	switch p.Default {
	case Allow, Deny, RequireApproval:
	default:
		return fmt.Errorf("policy: unknown default action %q", p.Default)
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		switch r.Action {
		case Allow, Deny, RequireApproval:
		default:
			return fmt.Errorf("policy: rule %d has unknown action %q", i, r.Action)
		}
		if r.Args != "" {
			re, err := regexp.Compile(r.Args)
			if err != nil {
				return fmt.Errorf("policy: rule %d args: %w", i, err)
			}
			r.argsRe = re
		}
		r.serverRe = compileWildcard(r.Server)
		r.toolRe = compileWildcard(r.Tool)
	}
	return nil
}

// Request is the call being evaluated.
type Request struct {
	Server string
	Tool   string
	Args   string
}

// Decision is the outcome of evaluating a request.
type Decision struct {
	Action Action
	Mode   Mode
	Rule   int
	Reason string
}

// Decide returns the first matching rule, or the default action.
func (p *Policy) Decide(r Request) Decision {
	if p == nil {
		return Decision{Action: Allow, Mode: ModeEnforce, Rule: -1, Reason: "no policy"}
	}
	args, canonical := "", false
	for i := range p.Rules {
		rule := &p.Rules[i]
		if !matchCompiled(rule.serverRe, rule.Server, r.Server) || !matchCompiled(rule.toolRe, rule.Tool, r.Tool) {
			continue
		}
		if rule.Args != "" {
			re := rule.argsRe
			if re == nil { // a Policy built in code without Load
				var err error
				if re, err = regexp.Compile(rule.Args); err != nil {
					continue
				}
			}
			if !canonical {
				args, canonical = CanonicalArgs(r.Args), true
			}
			if !re.MatchString(args) {
				continue
			}
		}
		reason := rule.Reason
		if reason == "" {
			reason = fmt.Sprintf("rule %d: %s", i, rule.Action)
		}
		return Decision{Action: rule.Action, Mode: p.Mode, Rule: i, Reason: reason}
	}
	return Decision{Action: p.Default, Mode: p.Mode, Rule: -1, Reason: "default policy"}
}

// DecisionRecord is one audit entry.
type DecisionRecord struct {
	SessionID string    `json:"sessionId"`
	At        time.Time `json:"at"`
	Server    string    `json:"server"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	CallID    string    `json:"callId,omitempty"`
	Action    Action    `json:"action"`
	Mode      Mode      `json:"mode"`
	Approved  bool      `json:"approved"`
	Reason    string    `json:"reason"`
}

// Auditor persists decisions.
type Auditor interface {
	Record(DecisionRecord) error
}

// ApprovalRequest is shown to a human when a call needs approval.
type ApprovalRequest struct {
	Server string
	Tool   string
	Args   string
	Reason string
}

// Approver asks a human to allow a call.
type Approver interface {
	Approve(ApprovalRequest) bool
}

// TimeoutApprover bounds how long a human has to answer. A missing or slow answer
// is a denial, so an unattended session can never hang forever.
type TimeoutApprover struct {
	Inner   Approver
	Timeout time.Duration
}

// Approve runs the inner approver with a deadline.
func (t TimeoutApprover) Approve(r ApprovalRequest) bool {
	if t.Inner == nil {
		return false
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}

	result := make(chan bool, 1)
	go func() { result <- t.Inner.Approve(r) }()

	select {
	case ok := <-result:
		return ok
	case <-time.After(timeout):
		return false
	}
}

// matchCompiled uses the glob compiled by validate, and falls back to compiling it on
// the spot for a Policy built in code without Load.
func matchCompiled(re *regexp.Regexp, pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if re != nil {
		return re.MatchString(s)
	}
	return wildcard(pattern, s)
}

func compileWildcard(pattern string) *regexp.Regexp {
	if pattern == "" || pattern == "*" {
		return nil
	}
	re, err := regexp.Compile(wildcardPattern(pattern))
	if err != nil {
		return nil
	}
	return re
}

// CanonicalArgs re-encodes JSON arguments so equivalent spellings look the same to an
// args regex: escapes such as \u0020 or \/ are decoded, whitespace between tokens is
// dropped, and <, > and & are not escaped. Text that is not JSON is returned as is.
//
// Rules are written against the compact form, e.g. "command":"rm -rf ...".
func CanonicalArgs(raw string) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return raw
	}
	return strings.TrimRight(buf.String(), "\n")
}

// wildcard matches a glob where * matches any run of characters and ? matches one.
func wildcard(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	re, err := regexp.Compile(wildcardPattern(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

func wildcardPattern(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}
