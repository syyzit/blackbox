package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
)

// A database created by an older build must keep working after upgrade.
func TestMigrationAddsNewColumns(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "blackbox.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, argv TEXT NOT NULL, pid INTEGER NOT NULL DEFAULT 0, hostname TEXT NOT NULL DEFAULT '', server_key TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, seq INTEGER NOT NULL, at INTEGER NOT NULL, direction TEXT NOT NULL, method TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL DEFAULT '', rpc_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '', result_type TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, args TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', raw TEXT NOT NULL DEFAULT '')`,
	}
	for _, stmt := range old {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatalf("open after upgrade: %v", err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo",
		Status: event.StatusPending, ToolHash: "abc123", Raw: `{}`,
	}); err != nil {
		t.Fatalf("emit after upgrade: %v", err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	_, events, err := Find(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ToolHash != "abc123" {
		t.Fatalf("new column not usable after migration: %+v", events)
	}
}
