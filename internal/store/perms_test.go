package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRecordingsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses ACLs, not mode bits")
	}
	home := filepath.Join(t.TempDir(), "bb")
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	sess.Ledger().Observe("srv", "read_file", "aaaa") // writes tooldefs.json
	defer sess.Close()

	check := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", filepath.Base(path), got, want)
		}
	}
	check(home, 0o700)
	check(filepath.Join(home, "blackbox.db"), 0o600)
	check(filepath.Join(home, "tooldefs.json"), 0o600)
	if _, err := os.Stat(filepath.Join(home, "blackbox.db-wal")); err == nil {
		check(filepath.Join(home, "blackbox.db-wal"), 0o600)
	}
}

func TestExistingDatabaseIsTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses ACLs, not mode bits")
	}
	home := t.TempDir()
	db := filepath.Join(home, "blackbox.db")
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	sess.Close()
	fi, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("database left at %o", fi.Mode().Perm())
	}
}
