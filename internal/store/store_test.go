package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
)

func TestLedgerDetectsChangedDefinitionsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	server := ServerKey([]string{"fake-server", "--root", "/tmp"})

	l := mustLoad(t, path)
	if l.Observe(server, "read_file", "aaaaaaaaaaaaaaaa") {
		t.Fatal("first observation must not be a change")
	}
	if !l.Observe(server, "read_file", "bbbbbbbbbbbbbbbb") {
		t.Fatal("changed definition was not detected")
	}
	if l.Observe(server, "write_file", "cccccccccccccccc") {
		t.Fatal("first observation of a new tool must not be a change")
	}
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}

	// A fresh process sees the persisted state.
	l2 := mustLoad(t, path)
	if !l2.Observe(server, "read_file", "dddddddddddddddd") {
		t.Fatal("ledger did not persist across reloads")
	}
	if l2.Observe(server, "write_file", "cccccccccccccccc") {
		t.Fatal("unchanged definition flagged after reload")
	}
}

func TestToolDefChangesArePersisted(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	err = sess.Emit(event.Event{
		Direction: event.ServerToClient,
		Method:    "tools/list",
		ToolDefs: []event.ToolDef{
			{Name: "read_file", Hash: "aaaaaaaaaaaaaaaa", Changed: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	_, events, err := Find(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].ToolDefs) != 1 || !events[0].ToolDefs[0].Changed {
		t.Fatalf("changed flag was not persisted: %+v", events)
	}
}

func TestPolicyDecisionsAreAudited(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec := policy.DecisionRecord{
		Server: "srv", Tool: "write_file", Args: `{"path":"x"}`,
		Action: policy.Deny, Approved: false, Reason: "writes need approval", At: now,
	}
	if err := sess.Record(rec); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	decisions, err := Decisions(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	got := decisions[0]
	if got.SessionID != id || got.Tool != "write_file" || got.Action != policy.Deny || got.Reason == "" {
		t.Fatalf("decision was not round-tripped: %+v", got)
	}
}

func TestServerKeyIsStableAndCommandSpecific(t *testing.T) {
	a := ServerKey([]string{"node", "server.js"})
	b := ServerKey([]string{"node", "server.js"})
	c := ServerKey([]string{"node", "other.js"})
	if a != b {
		t.Fatal("server key is not stable")
	}
	if a == c {
		t.Fatal("server key does not depend on the command")
	}
	if len(a) != 16 {
		t.Fatalf("unexpected key length %d", len(a))
	}
}
