package store

import (
	"strings"
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/coverage"
	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/policy"
)

func TestAppendExternalContinuesSequence(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()

	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "bash", Status: event.StatusPending, At: now, Args: `{"command":"echo hi"}`, Raw: `{}`},
		{Direction: event.ServerToClient, Method: "tools/call", Tool: "bash", Status: event.StatusOK, At: now, Result: `{"output":"hi"}`, Raw: `{}`},
	}
	if err := AppendExternal(home, "ses_abc", []string{"opencode", "builtins"}, events); err != nil {
		t.Fatal(err)
	}
	// A second call in the same external session must continue the sequence, not reset it.
	if err := AppendExternal(home, "ses_abc", []string{"opencode", "builtins"}, events); err != nil {
		t.Fatal(err)
	}

	list, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "oc-ses_abc" {
		t.Fatalf("unexpected sessions: %+v", list)
	}
	if len(list[0].Argv) != 2 || list[0].Argv[0] != "opencode" {
		t.Fatalf("argv = %v", list[0].Argv)
	}
	if list[0].StartedAt.After(now) {
		t.Fatalf("session started after its first event: %s > %s", list[0].StartedAt, now)
	}
	if list[0].Events != 4 {
		t.Fatalf("events = %d, want 4", list[0].Events)
	}

	_, stored, err := Find(home, "oc-ses_abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 || stored[3].Seq != 4 {
		t.Fatalf("sequence did not continue: %+v", stored)
	}
}

func TestRecordExternalDecision(t *testing.T) {
	home := t.TempDir()
	err := RecordExternalDecision(home, "ses_dec", policy.DecisionRecord{
		Server: "opencode", Tool: "shell", Action: policy.RequireApproval,
		Mode: policy.ModeShadow, Reason: "built-in shell", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The caller knows the OpenCode session id, not our oc- prefix.
	recs, err := Decisions(home, "ses_dec")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Mode != policy.ModeShadow || recs[0].Tool != "shell" {
		t.Fatalf("decision round trip failed: %+v", recs)
	}
}

func TestEventsBetweenSeesOtherSessions(t *testing.T) {
	home := t.TempDir()
	base := time.Now().UTC()

	// An MCP session and an external editor session overlap in time.
	mcp, err := Open(home, []string{"mcp-server"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "read_file",
		Status: event.StatusPending, At: base, Args: `{"path":"a.go"}`, Raw: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.Close(); err != nil {
		t.Fatal(err)
	}

	if err := AppendExternal(home, "ses_x", []string{"opencode", "builtins"}, []event.Event{{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "write",
		Status: event.StatusPending, At: base.Add(time.Second), Args: `{"path":"a.go"}`, Raw: `{}`,
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := EventsBetween(home, base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both sessions' events, got %+v", got)
	}

	rep := coverage.Classify("/repo", []coverage.Change{{Path: "a.go", Kind: coverage.Modified}}, got)
	if rep.Unrecorded != 0 || rep.Covered != 1 {
		t.Fatalf("external write did not cover the change: %+v", rep)
	}
}

func TestResolveDecision(t *testing.T) {
	home := t.TempDir()
	err := RecordExternalDecision(home, "ses_approve", policy.DecisionRecord{
		Server: "opencode", Tool: "shell", CallID: "call-9",
		Action: policy.RequireApproval, Mode: policy.ModeEnforce,
		Reason: "built-in shell", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := ResolveDecision(home, "ses_approve", "call-9", true, " (approved by user)"); err != nil {
		t.Fatal(err)
	}
	recs, err := Decisions(home, "ses_approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !recs[0].Approved || !strings.Contains(recs[0].Reason, "approved by user") {
		t.Fatalf("decision was not resolved: %+v", recs)
	}

	if err := ResolveDecision(home, "ses_approve", "missing", true, ""); err == nil {
		t.Fatal("expected an error for an unknown call id")
	}
}
