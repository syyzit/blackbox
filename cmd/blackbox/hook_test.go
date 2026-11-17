package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

// hookRun feeds one hook event to the adapter and returns its exit code and output.
func hookRun(t *testing.T, opts hookOptions, input string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := runHook(strings.NewReader(input), &out, &errw, opts)
	return code, out.String(), errw.String()
}

func writePolicy(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// permissionDecision extracts the decision and reason from a PreToolUse response.
func permissionDecision(t *testing.T, stdout string) (string, string) {
	t.Helper()
	var resp struct {
		Out struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a hook response: %v\n%s", err, stdout)
	}
	if resp.Out.Event != "PreToolUse" {
		t.Fatalf("hookEventName = %q", resp.Out.Event)
	}
	return resp.Out.Decision, resp.Out.Reason
}

const (
	hookPre = `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",
		"tool_input":{"command":"git push"},"tool_use_id":"toolu_1"}`
	hookPost = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash",
		"tool_input":{"command":"git push"},"tool_response":{"stdout":"done"},
		"tool_use_id":"toolu_1","duration_ms":42}`
)

func TestHookRecordsFinishedCall(t *testing.T) {
	home := t.TempDir()
	code, out, errw := hookRun(t, hookOptions{home: home}, hookPost)
	if code != 0 || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errw)
	}

	meta, events, err := store.Find(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Argv) != 2 || meta.Argv[0] != "claude-code" {
		t.Fatalf("argv = %v", meta.Argv)
	}
	if len(events) != 2 {
		t.Fatalf("want a request and a response, got %d events", len(events))
	}
	req, resp := events[0], events[1]
	if req.Tool != "Bash" || !strings.Contains(req.Args, "git push") || req.CallID != "toolu_1" {
		t.Fatalf("request = %+v", req)
	}
	if resp.Status != event.StatusOK || !strings.Contains(resp.Result, "done") || resp.DurationMS != 42 {
		t.Fatalf("response = %+v", resp)
	}
	if req.ExternalID != "s1" {
		t.Fatalf("external id = %q, want the Claude Code session id", req.ExternalID)
	}
}

func TestHookRecordsFailedCall(t *testing.T) {
	home := t.TempDir()
	code, _, errw := hookRun(t, hookOptions{home: home}, `{"session_id":"s1",
		"hook_event_name":"PostToolUseFailure","tool_name":"Read","tool_input":{"file_path":"x"},
		"tool_use_id":"toolu_2","error":"file not found"}`)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw)
	}
	_, events, err := store.Find(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Status != event.StatusError || events[1].Error != "file not found" {
		t.Fatalf("events = %+v", events)
	}
}

func TestHookSkipsMCPToolsUnlessAsked(t *testing.T) {
	const call = `{"session_id":"s1","hook_event_name":"PostToolUse",
		"tool_name":"mcp__files__read","tool_input":{},"tool_response":{},"tool_use_id":"toolu_3"}`

	home := t.TempDir()
	if code, _, _ := hookRun(t, hookOptions{home: home}, call); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if _, _, err := store.Find(home, "cc-s1"); err == nil {
		t.Fatal("an MCP tool was recorded, but the proxy already records those")
	}

	if code, _, _ := hookRun(t, hookOptions{home: home, mcp: true}, call); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if _, events, err := store.Find(home, "cc-s1"); err != nil || len(events) != 2 {
		t.Fatalf("with -mcp: events=%d err=%v", len(events), err)
	}
}

func TestMCPToolNames(t *testing.T) {
	cases := map[string][]string{
		"mcp__fake__echo":       {"echo"},
		"mcp__my__srv__do_it":   {"srv__do_it", "do_it"},
		"mcp__plugin_a_b__read": {"read"},
		"mcp__broken":           nil,
		"mcp____":               nil,
	}
	for in, want := range cases {
		got := mcpToolNames(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("mcpToolNames(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHookLinksProxiedMCPCallToTheSession(t *testing.T) {
	home := t.TempDir()
	mcp, err := store.Open(home, []string{"fakeserver"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", CallID: "call-1",
			Status: event.StatusPending, At: time.Now().UTC(), Args: `{"name":"echo","arguments":{"text":"hi"}}`, Raw: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "echo", CallID: "call-1",
			Status: event.StatusOK, At: time.Now().UTC(), Raw: `{}`},
	} {
		if err := mcp.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := mcp.Close(); err != nil {
		t.Fatal(err)
	}

	code, _, errw := hookRun(t, hookOptions{home: home}, `{"session_id":"s1",
		"hook_event_name":"PostToolUse","tool_name":"mcp__fake__echo","tool_input":{"text":"hi"},
		"tool_response":{},"tool_use_id":"toolu_5","duration_ms":12}`)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw)
	}
	tr, err := store.TraceFor(home, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 2 || tr.Events[0].Tool != "echo" {
		t.Fatalf("the proxied call did not join the Claude Code trace: %+v", tr.Events)
	}
	// Linking is not recording: without -mcp the call must not be stored a second time.
	if _, _, err := store.Find(home, "cc-s1"); err == nil {
		t.Fatal("the MCP call was also recorded as a built-in call")
	}
}

func TestHookAllowsQuietly(t *testing.T) {
	home := t.TempDir()
	// No policy file: everything is allowed, and the hook must say nothing so that
	// Claude Code's own permission rules still decide.
	code, out, errw := hookRun(t, hookOptions{home: home}, hookPre)
	if code != 0 || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errw)
	}
}

func TestHookDenies(t *testing.T) {
	home := t.TempDir()
	writePolicy(t, home, `{"default":"allow","mode":"enforce","rules":[
		{"server":"claude-code","tool":"Bash","match":{"command":"git push*"},"action":"deny","reason":"no pushing"}]}`)

	code, out, _ := hookRun(t, hookOptions{home: home}, hookPre)
	if code != 0 {
		t.Fatalf("a block must be JSON on exit 0, got exit %d", code)
	}
	decision, reason := permissionDecision(t, out)
	if decision != "deny" || !strings.Contains(reason, "no pushing") {
		t.Fatalf("decision=%q reason=%q", decision, reason)
	}

	// A different command is not matched by the rule.
	other := strings.Replace(hookPre, "git push", "git status", 1)
	if _, out, _ := hookRun(t, hookOptions{home: home}, other); out != "" {
		t.Fatalf("unmatched call got a decision: %s", out)
	}
}

func TestHookShadowModeOnlyRecords(t *testing.T) {
	home := t.TempDir()
	writePolicy(t, home, `{"default":"allow","mode":"shadow","rules":[
		{"tool":"Bash","action":"deny","reason":"no shell"}]}`)

	code, out, _ := hookRun(t, hookOptions{home: home}, hookPre)
	if code != 0 || out != "" {
		t.Fatalf("shadow mode must not decide: code=%d stdout=%q", code, out)
	}
	decisions, err := store.Decisions(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != policy.Deny || decisions[0].Mode != policy.ModeShadow {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestHookApprovalFlow(t *testing.T) {
	home := t.TempDir()
	writePolicy(t, home, `{"default":"allow","mode":"enforce","rules":[
		{"tool":"Bash","action":"require_approval","reason":"shell needs a human"}]}`)

	_, out, _ := hookRun(t, hookOptions{home: home}, hookPre)
	decision, reason := permissionDecision(t, out)
	if decision != "ask" || !strings.Contains(reason, "shell needs a human") {
		t.Fatalf("decision=%q reason=%q", decision, reason)
	}
	decisions, err := store.Decisions(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Approved {
		t.Fatalf("before the call ran: %+v", decisions)
	}

	// The call running is the evidence that the user approved it.
	if code, _, errw := hookRun(t, hookOptions{home: home}, hookPost); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw)
	}
	decisions, err = store.Decisions(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || !decisions[0].Approved || !strings.Contains(decisions[0].Reason, "approved via permission flow") {
		t.Fatalf("after the call ran: %+v", decisions)
	}
}

func TestHookFinishedCallDoesNotApproveAllowedDecision(t *testing.T) {
	home := t.TempDir()
	hookRun(t, hookOptions{home: home}, hookPre) // allowed: no policy
	hookRun(t, hookOptions{home: home}, hookPost)

	decisions, err := store.Decisions(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || strings.Contains(decisions[0].Reason, "approved") {
		t.Fatalf("an allow decision was rewritten as an approval: %+v", decisions)
	}
}

func TestHookBlocksWhenPolicyIsUnreadable(t *testing.T) {
	home := t.TempDir()
	writePolicy(t, home, `{"rules": [`)

	code, out, _ := hookRun(t, hookOptions{home: home}, hookPre)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if decision, reason := permissionDecision(t, out); decision != "deny" || !strings.Contains(reason, "could not load") {
		t.Fatalf("decision=%q reason=%q", decision, reason)
	}
}

func TestHookIgnoresOtherEventsAndRejectsBadInput(t *testing.T) {
	home := t.TempDir()
	code, out, _ := hookRun(t, hookOptions{home: home}, `{"session_id":"s1","hook_event_name":"Stop"}`)
	if code != 0 || out != "" {
		t.Fatalf("Stop: code=%d stdout=%q", code, out)
	}
	// Exit 1 is Claude Code's non-blocking error: reported, and the tool still runs.
	if code, _, _ := hookRun(t, hookOptions{home: home}, `not json`); code != 1 {
		t.Fatalf("bad input: exit %d, want 1", code)
	}
	if code, _, _ := hookRun(t, hookOptions{home: home}, `{"hook_event_name":"PreToolUse"}`); code != 1 {
		t.Fatalf("missing fields: exit %d, want 1", code)
	}
}

func TestHookCapsHugeResults(t *testing.T) {
	home := t.TempDir()
	big := strings.Repeat("x", hookMaxField+1)
	input := `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read",
		"tool_input":{},"tool_response":"` + big + `","tool_use_id":"toolu_9"}`
	if code, _, errw := hookRun(t, hookOptions{home: home}, input); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw)
	}
	_, events, err := store.Find(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(events[1].Result, `"truncated":true`) || len(events[1].Result) > 200 {
		t.Fatalf("result was not capped: %d bytes", len(events[1].Result))
	}
}

func TestEditHookSettingsInstallsOnce(t *testing.T) {
	bin := `C:\Program Files\blackbox\blackbox.exe`
	first, err := editHookSettings(nil, bin, []string{"hook"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.ValidBytes(first) {
		t.Fatalf("invalid JSON:\n%s", first)
	}
	for _, ev := range hookEvents {
		groups := gjson.GetBytes(first, "hooks."+ev).Array()
		if len(groups) != 1 {
			t.Fatalf("%s: %d groups, want 1", ev, len(groups))
		}
		h := groups[0].Get("hooks.0")
		if groups[0].Get("matcher").String() != "*" || h.Get("command").String() != bin ||
			h.Get("args.0").String() != "hook" || h.Get("type").String() != "command" {
			t.Fatalf("%s: %s", ev, groups[0].Raw)
		}
	}

	if !hasBlackboxHook(first) || hasBlackboxHook([]byte(`{"hooks":{"Stop":[]}}`)) {
		t.Fatal("hasBlackboxHook does not recognise what install wrote")
	}

	second, err := editHookSettings(first, bin, []string{"hook"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatalf("installing twice changed the file:\n%s", second)
	}

	// A moved binary or new flags replace the old entry instead of adding a second.
	moved, err := editHookSettings(first, "/opt/bin/blackbox", []string{"hook", "-mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(gjson.GetBytes(moved, "hooks.PreToolUse").Array()); n != 1 {
		t.Fatalf("reinstall left %d groups", n)
	}
	if got := gjson.GetBytes(moved, "hooks.PreToolUse.0.hooks.0.args.1").String(); got != "-mcp" {
		t.Fatalf("args not updated: %q", got)
	}
}

func TestEditHookSettingsKeepsEverythingElse(t *testing.T) {
	const existing = `{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "./lint.sh"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "./notify.sh"}]}
    ]
  }
}`
	installed, err := editHookSettings([]byte(existing), "blackbox", []string{"hook"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.ValidBytes(installed) {
		t.Fatalf("invalid JSON:\n%s", installed)
	}
	pre := gjson.GetBytes(installed, "hooks.PreToolUse").Array()
	if len(pre) != 2 || pre[0].Get("hooks.0.command").String() != "./lint.sh" {
		t.Fatalf("the user's own PreToolUse hook was not kept first: %s", gjson.GetBytes(installed, "hooks.PreToolUse").Raw)
	}
	if gjson.GetBytes(installed, "hooks.Stop.0.hooks.0.command").String() != "./notify.sh" ||
		gjson.GetBytes(installed, "permissions.allow.0").String() != "Bash(go test:*)" {
		t.Fatalf("unrelated settings changed:\n%s", installed)
	}

	removed, err := editHookSettings(installed, "blackbox", []string{"hook"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(removed), "blackbox") {
		t.Fatalf("remove left a blackbox entry:\n%s", removed)
	}
	if len(gjson.GetBytes(removed, "hooks.PreToolUse").Array()) != 1 ||
		gjson.GetBytes(removed, "hooks.PostToolUse").Exists() ||
		!gjson.GetBytes(removed, "hooks.Stop").Exists() {
		t.Fatalf("remove did not restore the original hooks:\n%s", removed)
	}
}

func TestEditHookSettingsRemoveLeavesNoEmptyHooks(t *testing.T) {
	installed, err := editHookSettings([]byte(`{"model":"x"}`), "blackbox", []string{"hook"}, false)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := editHookSettings(installed, "blackbox", []string{"hook"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(removed, "hooks").Exists() || gjson.GetBytes(removed, "model").String() != "x" {
		t.Fatalf("after remove:\n%s", removed)
	}
}

func TestEditHookSettingsRefusesWhatItCannotParse(t *testing.T) {
	for _, bad := range []string{`[1,2]`, `{"hooks": {"PreToolUse": "nope"}}`, `{not json`} {
		if _, err := editHookSettings([]byte(bad), "blackbox", []string{"hook"}, false); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
