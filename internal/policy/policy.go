// Package policy decides whether a tool call may proceed.
package policy

import (
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

// Rule matches tool calls and returns an action. Empty fields match anything.
type Rule struct {
	Server string `json:"server,omitempty"` // wildcard over the server command
	Tool   string `json:"tool,omitempty"`   // wildcard over the tool name
	Args   string `json:"args,omitempty"`   // regex over the raw arguments
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`

	argsRe *regexp.Regexp
}

// Policy is the on-disk policy file.
type Policy struct {
	Default      Action `json:"default"`
	RugPullBlock bool   `json:"rugPullBlock"`
	Rules        []Rule `json:"rules"`
}

// Default returns a permissive policy that still blocks changed tool definitions.
func Default() *Policy {
	return &Policy{Default: Allow, RugPullBlock: true}
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
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Policy) validate() error {
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
	Rule   int
	Reason string
}

// Decide returns the first matching rule, or the default action.
func (p *Policy) Decide(r Request) Decision {
	if p == nil {
		return Decision{Action: Allow, Rule: -1, Reason: "no policy"}
	}
	for i := range p.Rules {
		rule := &p.Rules[i]
		if !wildcard(rule.Server, r.Server) || !wildcard(rule.Tool, r.Tool) {
			continue
		}
		if rule.argsRe != nil && !rule.argsRe.MatchString(r.Args) {
			continue
		}
		reason := rule.Reason
		if reason == "" {
			reason = fmt.Sprintf("rule %d: %s", i, rule.Action)
		}
		return Decision{Action: rule.Action, Rule: i, Reason: reason}
	}
	return Decision{Action: p.Default, Rule: -1, Reason: "default policy"}
}

// DecisionRecord is one audit entry.
type DecisionRecord struct {
	SessionID string    `json:"sessionId"`
	At        time.Time `json:"at"`
	Server    string    `json:"server"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Action    Action    `json:"action"`
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

// wildcard matches a glob where * matches any run of characters and ? matches one.
func wildcard(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
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
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}
