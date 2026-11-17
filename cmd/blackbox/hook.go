package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

// hookMaxInput bounds the hook's stdin. A tool result can be large; anything over the
// limit is reported and skipped rather than recorded half-parsed (see hookOversized).
const hookMaxInput = 16 << 20

// hookMaxField is the largest tool input or result recorded verbatim.
const hookMaxField = 1 << 20

// hookInput is what the adapter uses of the JSON Claude Code writes to a command
// hook's stdin; see https://code.claude.com/docs/en/hooks.
type hookInput struct {
	SessionID    string
	Event        string
	ToolName     string
	ToolInput    json.RawMessage
	ToolResponse json.RawMessage
	ToolUseID    string
	Error        string
	DurationMS   int64
	AgentType    string
}

// parseHookFields splits the envelope into its top-level fields without interpreting
// them. Keys are matched exactly: decoding into a struct would match them without
// regard to case, letting a stray "Hook_Event_Name" replace the real one.
func parseHookFields(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("not a JSON object")
	}
	return fields, nil
}

// hookString reads a field the adapter cannot work without. Absent or null is "";
// any other type is an error.
func hookString(fields map[string]json.RawMessage, key string) (string, error) {
	raw := fields[key]
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s is not a string", key)
	}
	return s, nil
}

// hookInputFrom reads the fields beyond the three the decision depends on. They are
// read leniently: the policy decision is made on PreToolUse, where an error exit lets
// the tool run, so a surprise in a field used only for the record (a fractional
// duration, an error that is an object) must not turn into "no decision".
func hookInputFrom(fields map[string]json.RawMessage, event, session, tool string) hookInput {
	return hookInput{
		SessionID:    session,
		Event:        event,
		ToolName:     tool,
		ToolInput:    fields["tool_input"],
		ToolResponse: fields["tool_response"],
		ToolUseID:    lenientText(fields["tool_use_id"], false),
		Error:        lenientText(fields["error"], true),
		DurationMS:   lenientMillis(fields["duration_ms"]),
		AgentType:    lenientText(fields["agent_type"], false),
	}
}

// lenientText is a string field's value, or a number's or boolean's text. An object
// or array is its JSON text when structured is set (an error worth recording) and
// empty otherwise (an id that is not an id).
func lenientText(raw json.RawMessage, structured bool) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if text[0] == '{' || text[0] == '[' {
		if !structured {
			return ""
		}
		return compactJSON(raw)
	}
	return text
}

// lenientMillis reads a duration written as any JSON number, or as a string holding
// one, rounded to whole milliseconds. Anything else is 0, which means "unknown".
func lenientMillis(raw json.RawMessage) int64 {
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0
		}
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
			return 0
		}
	}
	// Also false for NaN, so the conversion below is always defined.
	if !(f > 0 && f < float64(math.MaxInt64)) {
		return 0
	}
	return int64(math.Round(f))
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
// error that does not block the tool. It never exits 2. A block is always a JSON
// "deny", so the reason reaches the model the same way on every platform.
func runHook(in io.Reader, out, errw io.Writer, opts hookOptions) int {
	data, err := io.ReadAll(io.LimitReader(in, hookMaxInput+1))
	if err != nil {
		fmt.Fprintln(errw, "blackbox hook:", err)
		return 1
	}
	if len(data) > hookMaxInput {
		return hookOversized(data, out, errw, opts)
	}
	fields, err := parseHookFields(data)
	if err != nil {
		fmt.Fprintln(errw, "blackbox hook: invalid JSON:", err)
		return 1
	}
	event, err := hookString(fields, "hook_event_name")
	if err != nil {
		fmt.Fprintln(errw, "blackbox hook:", err)
		return 1
	}
	switch event {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
	default:
		return 0 // registered for an event this adapter has nothing to do with
	}
	session, sessionErr := hookString(fields, "session_id")
	tool, toolErr := hookString(fields, "tool_name")
	if err := errors.Join(sessionErr, toolErr); err != nil {
		fmt.Fprintln(errw, "blackbox hook:", err)
		return 1
	}
	if session == "" || tool == "" {
		fmt.Fprintln(errw, "blackbox hook: session_id and tool_name are required")
		return 1
	}
	h := hookInputFrom(fields, event, session, tool)
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

// hookOversized handles input over the size limit, which is not parsed and so cannot
// be checked against the policy or recorded.
//
// For a finished call that only costs the recording. For a call about to run it
// would be a way past the policy: exit 1 lets the tool go ahead, so a large enough
// input would skip every rule. When rules are being enforced, such a call is denied.
func hookOversized(data []byte, out, errw io.Writer, opts hookOptions) int {
	failOpen := func() int {
		fmt.Fprintf(errw, "blackbox hook: input over %s, not recorded\n", humanBytes(hookMaxInput))
		return 1
	}

	// What can be read of the envelope before the cut. The event may be missing if
	// the oversized field came first; that is treated as a call about to run.
	fields := hookPrefixFields(data)
	event, _ := hookString(fields, "hook_event_name")
	session, _ := hookString(fields, "session_id")
	tool, _ := hookString(fields, "tool_name")
	if event != "" && event != "PreToolUse" {
		return failOpen()
	}
	if strings.HasPrefix(tool, "mcp__") && !opts.mcp {
		return failOpen() // the proxy gates wrapped servers; see hookOptions.mcp
	}

	pol, err := loadPolicy(opts.home, opts.policyPath)
	if err != nil {
		return hookDecision(out, "deny", fmt.Sprintf(
			"blackbox could not load its policy (%v). Fix or remove the file to continue.", err))
	}
	if !policyRestricts(pol) {
		return failOpen()
	}

	reason := fmt.Sprintf("this call's input is too large for blackbox to check (over %s), "+
		"and a policy is being enforced, so it was blocked. Retry with a smaller input.", humanBytes(hookMaxInput))
	if session != "" && tool != "" {
		if err := store.RecordExternalDecision(opts.home, store.ClaudeCode, session, policy.DecisionRecord{
			Server: store.ClaudeCode.Name, Tool: tool, Args: `{"truncated":true}`,
			CallID: lenientText(fields["tool_use_id"], false),
			Action: policy.Deny, Mode: policy.ModeEnforce, Reason: reason,
			At: time.Now().UTC(),
		}); err != nil {
			fmt.Fprintln(errw, "blackbox hook:", err)
		}
	}
	return hookDecision(out, "deny", "blackbox policy: "+reason)
}

// hookPrefixFields returns the complete top-level fields at the start of a JSON
// object that may be cut off part-way.
func hookPrefixFields(data []byte) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fields
	}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			break
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			break
		}
		fields[key] = value
	}
	return fields
}

// policyRestricts reports whether a policy can block anything: it is enforced, and
// its default or one of its rules is something other than allow.
func policyRestricts(p *policy.Policy) bool {
	if p == nil || p.Mode == policy.ModeShadow {
		return false
	}
	if p.Default != policy.Allow {
		return true
	}
	for _, rule := range p.Rules {
		if rule.Action != policy.Allow {
			return true
		}
	}
	return false
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
