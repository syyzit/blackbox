package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustLoad(t *testing.T, path string) *Ledger {
	t.Helper()
	l, err := LoadLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerPersistsWithoutExplicitSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")

	// A process that is killed never reaches Save; the first observation must
	// already be on disk.
	mustLoad(t, path).Observe("srv", "read_file", "aaaa")

	if !mustLoad(t, path).Observe("srv", "read_file", "bbbb") {
		t.Fatal("a changed definition in a later process was not detected")
	}
}

func TestLedgerKeepsFlaggingUntilAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	mustLoad(t, path).Observe("srv", "read_file", "aaaa")

	if !mustLoad(t, path).Observe("srv", "read_file", "bbbb") {
		t.Fatal("change not detected")
	}
	// Restarting must not quietly approve the new definition.
	l := mustLoad(t, path)
	if !l.Observe("srv", "read_file", "bbbb") {
		t.Fatal("a restart approved the changed definition")
	}
	if got := l.PendingChanges(); len(got) != 1 || got[0].Pending != "bbbb" || got[0].Approved != "aaaa" {
		t.Fatalf("pending changes = %+v", got)
	}

	accepted, err := mustLoad(t, path).Accept("", "read_file")
	if err != nil || len(accepted) != 1 {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	after := mustLoad(t, path)
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
	a := mustLoad(t, path)
	b := mustLoad(t, path)
	a.Observe("srv-a", "read_file", "aaaa")
	b.Observe("srv-b", "list", "bbbb")

	l := mustLoad(t, path)
	if _, ok := l.Known("srv-a", "read_file"); !ok {
		t.Fatal("first writer's entry was lost")
	}
	if _, ok := l.Known("srv-b", "list"); !ok {
		t.Fatal("second writer's entry was lost")
	}
}

func TestLedgerRevertClearsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	mustLoad(t, path).Observe("srv", "read_file", "aaaa")
	mustLoad(t, path).Observe("srv", "read_file", "bbbb")

	// The server goes back to the approved definition.
	if mustLoad(t, path).Observe("srv", "read_file", "aaaa") {
		t.Fatal("approved definition flagged")
	}
	if got := mustLoad(t, path).PendingChanges(); len(got) != 0 {
		t.Fatalf("revert left a pending change behind: %+v", got)
	}
}

func TestLedgerSeesExternalAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")

	// This process holds a ledger open for the life of a session.
	running := mustLoad(t, path)
	mustLoad(t, path).Observe("srv", "read_file", "aaaa")
	mustLoad(t, path).Observe("srv", "read_file", "bbbb")
	time.Sleep(2 * time.Millisecond)

	if !running.IsPending("srv", "read_file") {
		t.Fatal("running ledger did not see the pending change")
	}

	// Another process accepts it.
	time.Sleep(2 * time.Millisecond)
	if _, err := mustLoad(t, path).Accept("srv", "read_file"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	if running.IsPending("srv", "read_file") {
		t.Fatal("running ledger did not notice the accept")
	}
}

func TestCorruptLedgerFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLedger(path); err == nil {
		t.Fatal("a corrupt ledger was accepted")
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "tooldefs.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home, []string{"srv"}); err == nil {
		t.Fatal("Open accepted a corrupt ledger")
	}
}
