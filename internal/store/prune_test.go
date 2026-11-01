package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
)

func makeSession(t *testing.T, home, label string, size int, at time.Time) string {
	t.Helper()
	sess, err := Open(home, []string{label})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo",
		Status: event.StatusPending, Size: size, Raw: `{"jsonrpc":"2.0"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ServerToClient, Method: "tools/call", Tool: "echo",
		Status: event.StatusOK, Size: size, Raw: `{"result":{},"jsonrpc":"2.0"}`,
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	backdate(t, home, id, at)
	return id
}

func backdate(t *testing.T, home, id string, at time.Time) {
	t.Helper()
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, at.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
}

func TestPruneByAgeCascadesAndVacuum(t *testing.T) {
	home := t.TempDir()
	now := time.Now()

	// One old session with events, a tool definition and an audit row.
	sess, err := Open(home, []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", Status: event.StatusPending, Size: 100, Raw: `{"jsonrpc":"2.0"}`},
		{Direction: event.ServerToClient, Method: "tools/list", Status: event.StatusOK, Size: 100, Raw: `{"result":{}}`, ToolDefs: []event.ToolDef{{Name: "echo", Hash: "aaaaaaaaaaaaaaaa"}}},
	} {
		if err := sess.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Record(policy.DecisionRecord{
		Server: "srv", Tool: "echo", Action: policy.Deny, Reason: "test", At: now.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	oldID := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	backdate(t, home, oldID, now.Add(-72*time.Hour))

	keepID := makeSession(t, home, "keep", 100, now.Add(-1*time.Hour))

	res, err := Prune(home, PruneOptions{OlderThan: 24 * time.Hour, Vacuum: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != oldID {
		t.Fatalf("expected only the old session, got %+v", res.Sessions)
	}
	if !res.Vacuumed {
		t.Fatal("vacuum was requested but not reported")
	}

	if _, _, err := Find(home, oldID); err == nil {
		t.Fatal("old session still present")
	}
	if _, _, err := Find(home, keepID); err != nil {
		t.Fatalf("recent session was deleted: %v", err)
	}

	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`SELECT COUNT(*) FROM policy_decisions`,
		`SELECT COUNT(*) FROM tool_defs`,
		`SELECT COUNT(*) FROM events WHERE session_id = '` + oldID + `'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s returned %d rows, expected the cascade to remove them", q, n)
		}
	}
}

func TestPruneBySizeKeepsNewest(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	oldest := makeSession(t, home, "oldest", 100, now.Add(-3*time.Hour))
	middle := makeSession(t, home, "middle", 100, now.Add(-2*time.Hour))
	newest := makeSession(t, home, "newest", 100, now.Add(-1*time.Hour))

	// 600 bytes of events, budget 250: delete the two oldest.
	res, err := Prune(home, PruneOptions{MaxBytes: 250})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("expected 2 sessions deleted, got %+v", res.Sessions)
	}
	if _, _, err := Find(home, newest); err != nil {
		t.Fatalf("newest was deleted: %v", err)
	}
	for _, id := range []string{oldest, middle} {
		if _, _, err := Find(home, id); err == nil {
			t.Fatalf("expected %s to be deleted", id)
		}
	}
}

func TestPruneDryRunChangesNothing(t *testing.T) {
	home := t.TempDir()
	id := makeSession(t, home, "old", 100, time.Now().Add(-48*time.Hour))

	res, err := Prune(home, PruneOptions{OlderThan: time.Hour, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("dry run did not report the candidate: %+v", res.Sessions)
	}
	if _, _, err := Find(home, id); err != nil {
		t.Fatalf("dry run deleted a session: %v", err)
	}
}

func TestPruneRequiresAPolicy(t *testing.T) {
	if _, err := Prune(t.TempDir(), PruneOptions{}); err == nil {
		t.Fatal("expected an error without older-than or max-size")
	}
}
