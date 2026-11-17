package main

import (
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/store"
)

const denyBash = `{"default":"allow","mode":"enforce","rules":[
	{"server":"claude-code","tool":"Bash","action":"deny","reason":"no shell"}]}`

// A deny rule must hold whatever shape the fields it does not need arrive in. A
// failed decode exits 1, which Claude Code treats as "carry on".
func TestHookDecisionSurvivesFieldTypeSurprises(t *testing.T) {
	cases := map[string]string{
		"fractional duration": `"duration_ms":12.5`,
		"duration as string":  `"duration_ms":"12"`,
		"duration as object":  `"duration_ms":{"ms":12}`,
		"error as object":     `"error":{"code":"ENOENT","message":"missing"}`,
		"error as null":       `"error":null`,
		"numeric tool use id": `"tool_use_id":7`,
		"agent type as array": `"agent_type":["general"]`,
		"null response":       `"tool_response":null`,
		// Go's struct decoding matches keys without regard to case, so a stray key
		// in another case used to overwrite the real one.
		"event in another case": `"Hook_Event_Name":"Stop"`,
		"tool in another case":  `"TOOL_NAME":"Read"`,
		"session in other case": `"Session_Id":7`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writePolicy(t, home, denyBash)
			input := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",
				"tool_input":{"command":"rm -rf /"},` + extra + `}`
			code, out, errw := hookRun(t, hookOptions{home: home}, input)
			if code != 0 {
				t.Fatalf("exit %d (%s): the deny rule did not apply", code, strings.TrimSpace(errw))
			}
			if decision, _ := permissionDecision(t, out); decision != "deny" {
				t.Fatalf("decision = %q", decision)
			}
		})
	}
}

func TestHookStillRequiresTheFieldsItDecidesOn(t *testing.T) {
	home := t.TempDir()
	for _, input := range []string{
		`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":5}`,
		`{"session_id":["s1"],"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"session_id":"s1","hook_event_name":{"x":1},"tool_name":"Bash"}`,
		`["not","an","object"]`,
	} {
		if code, out, _ := hookRun(t, hookOptions{home: home}, input); code != 1 || out != "" {
			t.Errorf("%s: code=%d stdout=%q, want exit 1", input, code, out)
		}
	}
}

func TestHookRecordsLenientFields(t *testing.T) {
	home := t.TempDir()
	code, _, errw := hookRun(t, hookOptions{home: home}, `{"session_id":"s1",
		"hook_event_name":"PostToolUseFailure","tool_name":"Read","tool_input":{"file_path":"x"},
		"tool_use_id":"toolu_2","duration_ms":1500.4,"error":{"code":"ENOENT"}}`)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errw)
	}
	_, events, err := store.Find(home, "cc-s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Error != `{"code":"ENOENT"}` || events[1].DurationMS != 1500 {
		t.Fatalf("recorded error %q, duration %d", events[1].Error, events[1].DurationMS)
	}
}

// Input over the size limit cannot be checked against the policy. With rules being
// enforced, a call that cannot be checked is blocked, with a reason the user can act
// on, rather than waved through.
func TestHookOversizedInput(t *testing.T) {
	big := strings.Repeat("x", hookMaxInput)
	pre := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"` + big + `"}}`
	// Nothing says the event comes before the bulk of the input.
	preLate := `{"tool_input":{"command":"` + big + `"},"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash"}`
	post := `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":"` + big + `"}`
	const shadow = `{"default":"allow","mode":"shadow","rules":[{"tool":"Bash","action":"deny"}]}`

	cases := []struct {
		name, policy, input string
		wantDeny            string // part of the deny reason, or "" for no decision
	}{
		{"enforced policy blocks", denyBash, pre, "too large"},
		{"event after the oversized field", denyBash, preLate, "too large"},
		{"restrictive default blocks", `{"default":"deny"}`, pre, "too large"},
		{"unreadable policy blocks", `{not json`, pre, "could not load"},
		{"no policy fails open", "", pre, ""},
		{"allow-all policy fails open", `{"default":"allow"}`, pre, ""},
		{"shadow mode fails open", shadow, pre, ""},
		{"finished call is only reported", denyBash, post, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.policy != "" {
				writePolicy(t, home, tc.policy)
			}
			code, out, errw := hookRun(t, hookOptions{home: home}, tc.input)
			if tc.wantDeny == "" {
				if code != 1 || out != "" || !strings.Contains(errw, "not recorded") {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errw)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit %d: an oversized call got past an enforced policy", code)
			}
			decision, reason := permissionDecision(t, out)
			if decision != "deny" || !strings.Contains(reason, tc.wantDeny) {
				t.Fatalf("decision=%q reason=%q", decision, reason)
			}
		})
	}
}
