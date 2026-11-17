package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

// hookMaxInput bounds the hook's stdin. A tool result can be large; anything over the
// limit is reported and skipped rather than recorded half-parsed.
const hookMaxInput = 16 << 20

// hookMaxField is the largest tool input or result recorded verbatim.
const hookMaxField = 1 << 20

// hookInput is the JSON Claude Code writes to a command hook's stdin. Only the fields
// the adapter uses are listed; see https://code.claude.com/docs/en/hooks.
type hookInput struct {
	SessionID    string          `json:"session_id"`
	Event        string          `json:"hook_event_name"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolUseID    string          `json:"tool_use_id"`
	Error        string          `json:"error"`
	DurationMS   int64           `json:"duration_ms"`
	AgentType    string          `json:"agent_type"`
}

type hookOptions struct {
	home       string
	policyPath string
	// mcp also handles MCP tools (mcp__server__tool). It is off by default because a
	// server wrapped by `blackbox run` is already recorded and gated by the proxy.
	mcp bool
}

// cmdHook is the Claude Code adapter: one command registered for PreToolUse,
// PostToolUse and PostToolUseFailure. It does for Claude Code what
// plugins/opencode-blackbox does for OpenCode.
func cmdHook(args []string) int {
	if len(args) > 0 && args[0] == "install" {
		return cmdHookInstall(args[1:])
	}
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	var opts hookOptions
	fs.StringVar(&opts.home, "home", "", "data directory")
	fs.StringVar(&opts.policyPath, "policy", "", "policy file (default: <home>/policy.json)")
	fs.BoolVar(&opts.mcp, "mcp", false, "also record and check MCP tools (off: the proxy covers wrapped servers)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runHook(os.Stdin, os.Stdout, os.Stderr, opts)
}

// runHook handles one hook event.
//
// Exit codes follow Claude Code's contract: 0 with optional JSON on stdout, or 1 for an
// error that must not block the tool. It never exits 2. A block is always a JSON
// "deny", so the reason reaches the model the same way on every platform.
func runHook(in io.Reader, out, errw io.Writer, opts hookOptions) int {
	data, err := io.ReadAll(io.LimitReader(in, hookMaxInput+1))
	if err != nil {
		fmt.Fprintln(errw, "blackbox hook:", err)
		return 1
	}
	if len(data) > hookMaxInput {
		fmt.Fprintf(errw, "blackbox hook: input over %s, not recorded\n", humanBytes(hookMaxInput))
		return 1
	}
	var h hookInput
	if err := json.Unmarshal(data, &h); err != nil {
		fmt.Fprintln(errw, "blackbox hook: invalid JSON:", err)
		return 1
	}

	switch h.Event {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
	default:
		return 0 // registered for an event this adapter has nothing to do with
	}
	if h.SessionID == "" || h.ToolName == "" {
		fmt.Fprintln(errw, "blackbox hook: session_id and tool_name are required")
		return 1
	}
	if strings.HasPrefix(h.ToolName, "mcp__") {
		if h.Event != "PreToolUse" {
			hookLinkMCPCall(h, errw, opts)
		}
		if !opts.mcp {
			return 0
		}
	}

	if h.Event == "PreToolUse" {
		return hookBefore(h, out, opts)
	}
	return hookAfter(h, errw, opts)
}

// hookBefore applies the policy before a tool runs. An allowed call prints nothing, so
// Claude Code's own permission rules still apply to it: the policy can only tighten
// what the user has configured, never grant a call the user would have been asked about.
func hookBefore(h hookInput, out io.Writer, opts hookOptions) int {
	dec, err := decideExternalCall(opts.home, opts.policyPath, store.ClaudeCode, checkPayload{
		SessionID: h.SessionID,
		CallID:    h.ToolUseID,
		Tool:      h.ToolName,
		Agent:     h.AgentType,
		Input:     h.ToolInput,
	})
	if err != nil {
		// Same as `blackbox check`: a policy that cannot be read blocks the call. Running
		// unprotected because of a typo in policy.json would be the worse surprise.
		return hookDecision(out, "deny", fmt.Sprintf(
			"blackbox could not load its policy or redaction rules (%v). Fix or remove the file to continue.", err))
	}
	if dec.Mode == policy.ModeShadow {
		return 0
	}
	switch dec.Action {
	case policy.Deny:
		return hookDecision(out, "deny", "blackbox policy: "+dec.Reason)
	case policy.RequireApproval:
		// "ask" hands the call to Claude Code's permission prompt, so the approval
		// looks and behaves like every other one.
		return hookDecision(out, "ask", "blackbox policy: "+dec.Reason)
	default:
		return 0
	}
}

func hookDecision(out io.Writer, decision, reason string) int {
	_ = json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	})
	return 0
}

// hookAfter records a finished call. Recording fails open: an error is reported with a
// non-blocking exit code and the agent carries on.
func hookAfter(h hookInput, errw io.Writer, opts hookOptions) int {
	p := ingestPayload{
		Source:     store.ClaudeCode.Name,
		SessionID:  h.SessionID,
		CallID:     h.ToolUseID,
		Tool:       h.ToolName,
		Agent:      h.AgentType,
		Input:      capField(h.ToolInput),
		DurationMS: h.DurationMS,
		At:         time.Now().UTC(),
		Status:     "completed",
	}
	if h.Event == "PostToolUseFailure" {
		p.Status = "error"
		p.Error = h.Error
		if p.Error == "" {
			p.Error = "tool failed"
		}
	} else {
		p.Result = capField(h.ToolResponse)
		// The call ran, so if the policy had asked for approval, the user gave it. A
		// rejected prompt never reaches PostToolUse and its decision stays unapproved.
		if _, err := store.ApprovePending(opts.home, store.ClaudeCode, h.SessionID, h.ToolUseID,
			" (approved via permission flow)"); err != nil {
			fmt.Fprintln(errw, "blackbox hook:", err)
		}
	}

	if err := recordExternalCall(opts.home, store.ClaudeCode, p); err != nil {
		fmt.Fprintln(errw, "blackbox hook:", err)
		return 1
	}
	return 0
}

// hookLinkMCPCall ties the proxy's recording of an MCP call to this Claude Code
// session. Claude Code does not put its session id on MCP calls, so the proxy cannot
// know which agent session a call belongs to; the hook does, once the call is over.
// It is best effort: a server that is not wrapped has no recording to link.
func hookLinkMCPCall(h hookInput, errw io.Writer, opts hookOptions) {
	tools := mcpToolNames(h.ToolName)
	if len(tools) == 0 {
		return
	}
	red, err := loadRedactor(opts.home)
	if err != nil {
		return // the PreToolUse path reports a broken redact.json
	}
	arguments := red.String(compactJSON(h.ToolInput))

	now := time.Now().UTC()
	lookback := time.Duration(h.DurationMS)*time.Millisecond + 10*time.Second
	if h.DurationMS <= 0 {
		lookback = 2 * time.Minute
	}
	// The proxy writes the response frame as it forwards it, which can be a moment
	// after Claude Code has the result, so look twice.
	for attempt := 0; attempt < 2; attempt++ {
		linked, err := store.LinkExternalCall(opts.home, h.SessionID, tools, arguments, now.Add(-lookback), now.Add(time.Second))
		if err != nil {
			fmt.Fprintln(errw, "blackbox hook:", err)
			return
		}
		if linked || attempt == 1 {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// mcpToolNames lists the tool names the server may know an mcp__server__tool by. The
// name does not say where the server part ends, so every split point is a candidate.
func mcpToolNames(name string) []string {
	rest := strings.TrimPrefix(name, "mcp__")
	var out []string
	for i := 0; i+2 <= len(rest); i++ {
		if rest[i:i+2] == "__" && i > 0 && i+2 < len(rest) {
			out = append(out, rest[i+2:])
		}
	}
	return out
}

// capField replaces an oversized tool input or result with a marker, so one huge file
// read does not bloat the archive.
func capField(raw json.RawMessage) json.RawMessage {
	if len(raw) <= hookMaxField {
		return raw
	}
	return json.RawMessage(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(raw)))
}
