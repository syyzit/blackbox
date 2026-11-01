package store

import (
	"path/filepath"
	"testing"
)

func TestLedgerPersistsWithoutExplicitSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")

	// A process that is killed never reaches Save; the first observation must
	// already be on disk.
	LoadLedger(path).Observe("srv", "read_file", "aaaa")

	if !LoadLedger(path).Observe("srv", "read_file", "bbbb") {
		t.Fatal("a changed definition in a later process was not detected")
	}
}

func TestLedgerKeepsFlaggingUntilAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	LoadLedger(path).Observe("srv", "read_file", "aaaa")

	if !LoadLedger(path).Observe("srv", "read_file", "bbbb") {
		t.Fatal("change not detected")
	}
	// Restarting must not quietly approve the new definition.
	l := LoadLedger(path)
	if !l.Observe("srv", "read_file", "bbbb") {
		t.Fatal("a restart approved the changed definition")
	}
	if got := l.PendingChanges(); len(got) != 1 || got[0].Pending != "bbbb" || got[0].Approved != "aaaa" {
		t.Fatalf("pending changes = %+v", got)
	}

	accepted, err := LoadLedger(path).Accept("", "read_file")
	if err != nil || len(accepted) != 1 {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	after := LoadLedger(path)
	if after.Observe("srv", "read_file", "bbbb") {
		t.Fatal("accepted definition is still flagged")
	}
	if len(after.PendingChanges()) != 0 {
		t.Fatal("pending change survived accept")
	}
}

func TestLedgerMergesConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")

	// Two wrapped servers load the ledger before either writes.
	a := LoadLedger(path)
	b := LoadLedger(path)
	a.Observe("srv-a", "read_file", "aaaa")
	b.Observe("srv-b", "list", "bbbb")

	l := LoadLedger(path)
	if _, ok := l.Known("srv-a", "read_file"); !ok {
		t.Fatal("first writer's entry was lost")
	}
	if _, ok := l.Known("srv-b", "list"); !ok {
		t.Fatal("second writer's entry was lost")
	}
}

func TestLedgerRevertClearsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	LoadLedger(path).Observe("srv", "read_file", "aaaa")
	LoadLedger(path).Observe("srv", "read_file", "bbbb")

	// The server goes back to the approved definition.
	if LoadLedger(path).Observe("srv", "read_file", "aaaa") {
		t.Fatal("approved definition flagged")
	}
	if got := LoadLedger(path).PendingChanges(); len(got) != 0 {
		t.Fatalf("revert left a pending change behind: %+v", got)
	}
}
