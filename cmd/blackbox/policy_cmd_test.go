package main

import (
	"encoding/json"
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
