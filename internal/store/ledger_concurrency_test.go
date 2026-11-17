package store

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A revert clears the pending entry; if the server then changes again, the new
// pending entry must survive, in this process and on disk.
func TestLedgerRepeatedChangeStaysPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	l := mustLoad(t, path)

	want := []bool{false, true, false, true}
	for i, hash := range []string{"aaaa", "bbbb", "aaaa", "bbbb"} {
		if got := l.Observe("srv", "read_file", hash); got != want[i] {
			t.Fatalf("observation %d (%s) = %v, want %v", i, hash, got, want[i])
		}
	}
	if !l.IsPending("srv", "read_file") {
		t.Fatal("the second change is not pending in the process that saw it")
	}
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, path).PendingChanges(); len(got) != 1 || got[0].Pending != "bbbb" {
		t.Fatalf("pending on disk = %+v", got)
	}
}

// One process saw a change and its revert; another then sees the change again. An
// unrelated save from the first must not erase the second's pending entry.
func TestLedgerRevertDoesNotEraseAnotherProcessesPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	p1 := mustLoad(t, path)
	for _, hash := range []string{"aaaa", "bbbb", "aaaa"} {
		p1.Observe("srv", "read_file", hash)
	}

	p2 := mustLoad(t, path)
	if !p2.Observe("srv", "read_file", "bbbb") {
		t.Fatal("second process did not flag the change")
	}

	p1.Observe("srv", "other_tool", "cccc")
	if err := p1.Save(); err != nil {
		t.Fatal(err)
	}

	if !mustLoad(t, path).IsPending("srv", "read_file") {
		t.Fatal("the first process's save erased the pending change")
	}
	if !p2.IsPending("srv", "read_file") {
		t.Fatal("the second process lost its own pending change")
	}
}

// Observe cannot report a failed write, so what it learned has to stay in memory and
// reach the disk with the next write that works.
func TestLedgerObserveSurvivesFailedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	l := mustLoad(t, path)

	// A directory where the file should be makes every read and write fail.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	l.Observe("srv", "read_file", "aaaa")
	if !l.Observe("srv", "read_file", "bbbb") {
		t.Fatal("change not flagged while the ledger could not be written")
	}
	if !l.IsPending("srv", "read_file") {
		t.Fatal("pending change lost from memory after a failed save")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// Nothing new to learn here; the call only has to notice the unsaved state.
	if !l.Observe("srv", "read_file", "bbbb") {
		t.Fatal("change no longer flagged")
	}
	fresh := mustLoad(t, path)
	if h, ok := fresh.Known("srv", "read_file"); !ok || h != "aaaa" {
		t.Fatalf("approved hash on disk = %q, %v", h, ok)
	}
	if !fresh.IsPending("srv", "read_file") {
		t.Fatal("pending change never reached the disk")
	}
}

// Readers and writers in different processes share the file. On Windows a reader's
// open handle makes the writer's rename fail, and a read during the rename fails, so
// reads have to take the same lock as writes.
func TestLedgerConcurrentLoadAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	mustLoad(t, path).Observe("srv", "seed", "0000")

	const workers = 4
	deadline := time.Now().Add(500 * time.Millisecond)
	errs := make(chan error, 2*workers)
	for w := 0; w < workers; w++ {
		go func() {
			for time.Now().Before(deadline) {
				if _, err := LoadLedger(path); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
		go func(w int) {
			l, err := LoadLedger(path)
			if err != nil {
				errs <- err
				return
			}
			for i := 0; time.Now().Before(deadline); i++ {
				l.Observe("srv-"+strconv.Itoa(w), "tool-"+strconv.Itoa(i%8), "hash")
				if err := l.Save(); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(w)
	}
	for i := 0; i < 2*workers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	l := mustLoad(t, path)
	for w := 0; w < workers; w++ {
		if _, ok := l.Known("srv-"+strconv.Itoa(w), "tool-0"); !ok {
			t.Fatalf("writer %d's first observation is not on disk", w)
		}
	}
}
