//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLedgerLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tooldefs.json")
	unlock, err := lockLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	f, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("a second process could take the ledger lock")
	}
}
