package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spacecr8ed/blackbox/internal/policy"
	"github.com/spacecr8ed/blackbox/internal/redact"
	"github.com/spacecr8ed/blackbox/internal/store"
)

type checkPayload struct {
	SessionID string          `json:"sessionId"`
	Tool      string          `json:"tool"`
	Agent     string          `json:"agent"`
	Input     json.RawMessage `json:"input"`
}

// cmdCheck evaluates the policy for a tool call made outside the MCP proxy, such as a
// built-in editor or shell tool. Exit 0 allows, 1 denies, 2 needs approval. In shadow
// mode it always exits 0 and only records what would have happened.
func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	policyPath := fs.String("policy", "", "policy file (default: <home>/policy.json)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	var p checkPayload
	if err := json.Unmarshal(data, &p); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox: check: invalid JSON:", err)
		return 1
	}
	if p.SessionID == "" || p.Tool == "" {
		fmt.Fprintln(os.Stderr, "blackbox: check: sessionId and tool are required")
		return 1
	}

	pol, err := loadPolicy(*home, *policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	rawArgs := compactJSON(p.Input)
	dec := pol.Decide(policy.Request{Server: "opencode", Tool: p.Tool, Args: rawArgs})

	auditText := redact.Default().String(rawArgs)
	if len(auditText) > 4096 {
		auditText = auditText[:4096] + "…"
	}
	if err := store.RecordExternalDecision(*home, p.SessionID, policy.DecisionRecord{
		Server: "opencode", Tool: p.Tool, Args: auditText,
		Action: dec.Action, Mode: dec.Mode, Reason: dec.Reason,
		At: time.Now().UTC(),
	}); err != nil {
		// A decision that cannot be recorded must not break the tool call.
		fmt.Fprintf(os.Stderr, "blackbox: check: %v\n", err)
	}

	if dec.Mode == policy.ModeShadow {
		return 0
	}
	switch dec.Action {
	case policy.Deny:
		fmt.Fprintf(os.Stderr, "blackbox policy: %s\n", dec.Reason)
		return 1
	case policy.RequireApproval:
		fmt.Fprintf(os.Stderr, "blackbox policy: approval required: %s\n", dec.Reason)
		return 2
	default:
		return 0
	}
}
