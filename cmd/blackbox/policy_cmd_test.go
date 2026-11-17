package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

// `policy test` over a recorded built-in session has to give the decision the live
// check gave for the same call: same input, same server name.
func TestPolicyTestAgreesWithLiveCheckForBuiltins(t *testing.T) {
	home := t.TempDir()
	writePolicy(t, home, `{"default":"allow","rules":[
		{"server":"claude-code","tool":"Read","paths":{"file_path":"/etc/*"},"action":"deny"},
		{"tool":"Bash","match":{"command":"git push*"},"action":"require_approval"}
	]}`)

	calls := []struct {
		id, tool, input string
		want            policy.Action
	}{
		{"c1", "Read", `{"file_path":"/etc/passwd"}`, policy.Deny},
		{"c2", "Read", `{"file_path":"/tmp/notes.txt"}`, policy.Allow},
		{"c3", "Bash", `{"command":"git push origin main"}`, policy.RequireApproval},
	}
	for _, c := range calls {
		if err := recordExternalCall(home, store.ClaudeCode, ingestPayload{
			SessionID: "s1", CallID: c.id, Tool: c.tool, Input: json.RawMessage(c.input),
		}); err != nil {
			t.Fatal(err)
		}
	}

	pol, err := loadPolicy(home, "")
	if err != nil {
		t.Fatal(err)
	}
	meta, events, err := store.Find(home, store.ExternalID(store.ClaudeCode.Prefix, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	sum := simulatePolicy(pol, meta, events)
	if len(sum.Impacts) != len(calls) {
		t.Fatalf("impacts = %d, want %d", len(sum.Impacts), len(calls))
	}
	for i, c := range calls {
		live, err := decideExternalCall(home, "", store.ClaudeCode, checkPayload{
			SessionID: "s2", CallID: c.id, Tool: c.tool, Input: json.RawMessage(c.input),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := sum.Impacts[i].Decision; got != c.want || live.Action != c.want {
			t.Errorf("%s %s: simulated %s, live %s, want %s", c.tool, c.input, got, live.Action, c.want)
		}
	}
}

// Recorded arguments are redacted and the live check saw them unredacted, so a rule
// that reads arguments can decide differently in simulation. policy test says so when
// both are present, and only then.
func TestPolicyTestNotesRedactedArguments(t *testing.T) {
	redacted := policy.ImpactSummary{Impacts: []policy.Impact{
		{Tool: "Bash", Args: `{"command":"echo hi"}`},
		{Tool: "Bash", Args: `{"command":"curl -H 'Authorization: [REDACTED:secret]' x"}`},
	}}
	plain := policy.ImpactSummary{Impacts: redacted.Impacts[:1]}

	cases := []struct {
		name, policy string
		sum          policy.ImpactSummary
		want         bool
	}{
		{"args rule", `{"rules":[{"args":"sk-","action":"deny"}]}`, redacted, true},
		{"match rule", `{"rules":[{"match":{"command":"*sk-*"},"action":"deny"}]}`, redacted, true},
		{"paths rule", `{"rules":[{"paths":{"file_path":"/etc/*"},"action":"deny"}]}`, redacted, true},
		{"tool rules only", `{"rules":[{"tool":"Bash","action":"deny"}]}`, redacted, false},
		{"nothing redacted", `{"rules":[{"args":"sk-","action":"deny"}]}`, plain, false},
	}
	for _, tc := range cases {
		home := t.TempDir()
		writePolicy(t, home, tc.policy)
		pol, err := loadPolicy(home, "")
		if err != nil {
			t.Fatal(err)
		}
		note := redactionNote(pol, tc.sum)
		if (note != "") != tc.want {
			t.Errorf("%s: note %q, want one: %v", tc.name, note, tc.want)
		}
		if tc.want && !strings.Contains(note, "1 call") {
			t.Errorf("%s: note %q does not count the redacted call", tc.name, note)
		}
	}
}
