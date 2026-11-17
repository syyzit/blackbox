package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookInstallRemoveWithoutSettingsCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if code := cmdHookInstall([]string{"-dir", dir, "-remove"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("removing a hook that was never installed created %s", entries[0].Name())
	}
}

// Installing, changing and removing within one second must leave the file as the
// user wrote it in a backup, not overwritten by a later backup of the same name.
func TestHookInstallKeepsEveryBackup(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	const pristine = `{"model": "opus"}`
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(pristine), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"-dir", dir, "-command", "blackbox"},
		{"-dir", dir, "-command", "blackbox", "-mcp"},
		{"-dir", dir, "-remove"},
	} {
		if code := cmdHookInstall(args); code != 0 {
			t.Fatalf("hook install %v: exit %d", args, code)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(settings))
	if err != nil {
		t.Fatal(err)
	}
	backups, foundPristine := 0, false
	for _, e := range entries {
		switch {
		case e.Name() == filepath.Base(settings):
		case strings.Contains(e.Name(), ".blackbox-backup-"):
			backups++
			b, err := os.ReadFile(filepath.Join(filepath.Dir(settings), e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			foundPristine = foundPristine || string(b) == pristine
		default:
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
	if backups != 3 || !foundPristine {
		t.Fatalf("%d backups, original preserved: %v", backups, foundPristine)
	}
}
