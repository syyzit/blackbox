package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
)

func TestLedgerFlagsChangedToolDefinitions(t *testing.T) {
	home := t.TempDir()
	argv := []string{"fake-server", "--root", "/tmp"}

	first, err := Open(home, argv)
	if err != nil {
		t.Fatal(err)
	}
	err = first.Emit(event.Event{
		Direction: event.ServerToClient,
		ToolDefs: []event.ToolDef{
			{Name: "read_file", Hash: "aaaaaaaaaaaaaaaa"},
			{Name: "write_file", Hash: "bbbbbbbbbbbbbbbb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Same server, first tool's definition changed, second is new.
	second, err := Open(home, argv)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	got := []event.ToolDef{
		{Name: "read_file", Hash: "cccccccccccccccc"},
		{Name: "write_file", Hash: "bbbbbbbbbbbbbbbb"},
		{Name: "list_dir", Hash: "dddddddddddddddd"},
	}
	if err := second.Emit(event.Event{Direction: event.ServerToClient, ToolDefs: got}); err != nil {
		t.Fatal(err)
	}

	_, events, err := Find(home, second.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	defs := events[0].ToolDefs
	if len(defs) != 3 {
		t.Fatalf("expected 3 defs, got %+v", defs)
	}
	if !defs[0].Changed {
		t.Fatalf("changed definition was not flagged: %+v", defs[0])
	}
	if defs[1].Changed {
		t.Fatalf("unchanged definition was flagged: %+v", defs[1])
	}
	if defs[2].Changed {
		t.Fatalf("first-seen definition was flagged as changed: %+v", defs[2])
	}

	if _, err := os.Stat(filepath.Join(home, "tooldefs.json")); err != nil {
		t.Fatalf("ledger was not persisted: %v", err)
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
