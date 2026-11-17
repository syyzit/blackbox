package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink inside the root that points outside it is lexically inside, so the path
// checks resolve symlinks before deciding.
func TestPathChecksFollowSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		// Windows needs a privilege or developer mode to create symlinks.
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "in")); err != nil {
		t.Fatal(err)
	}

	denied := [][]string{
		{"cat", "out/secret"},
		{"cat", "out"},
		{"grep", "-r", "x", "out"},
		{"grep", "--file=out/secret", "."},
		{"cat", "out/missing/file"}, // not there yet, but it would be outside
		{"cat", filepath.Join(root, "out", "secret")},
	}
	for _, c := range denied {
		if err := checkArgs(root, root, c[0], c[1:]); err == nil || !strings.Contains(err.Error(), "outside the repo root") {
			t.Errorf("%v: got %v, want outside-root rejection", c, err)
		}
	}
	allowed := [][]string{
		{"cat", "in/file"},
		{"ls", "in"},
		{"grep", "-rn", "needle", "."},
	}
	for _, c := range allowed {
		if err := checkArgs(root, root, c[0], c[1:]); err != nil {
			t.Errorf("%v: unexpected rejection %v", c, err)
		}
	}

	s := &server{root: root}
	if _, err := s.resolve("out"); err == nil {
		t.Error("cwd through a symlink to outside the root was allowed")
	}
	if _, err := s.resolve("in"); err != nil {
		t.Errorf("cwd through a symlink inside the root rejected: %v", err)
	}
}
