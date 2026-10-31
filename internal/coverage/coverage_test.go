package coverage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDiff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "one")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "two")
	writeFile(t, filepath.Join(root, "node_modules", "noise.js"), "ignore me")

	before, err := Take(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Files["node_modules/noise.js"]; ok {
		t.Fatal("ignored directory was snapshotted")
	}

	writeFile(t, filepath.Join(root, "a.txt"), "one changed")
	writeFile(t, filepath.Join(root, "c.txt"), "new")
	if err := os.Remove(filepath.Join(root, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}

	after, err := Take(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	changes := Diff(before, after)
	want := map[string]string{
		"a.txt":     Modified,
		"c.txt":     Created,
		"sub/b.txt": Deleted,
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v", changes)
	}
	for _, ch := range changes {
		if want[ch.Path] != ch.Kind {
			t.Fatalf("change %s = %s, want %s", ch.Path, ch.Kind, want[ch.Path])
		}
	}
}

func TestClassify(t *testing.T) {
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "write_file", Status: event.StatusPending, Args: `{"path":"/repo/a.go"}`},
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "edit_file", Status: event.StatusPending, Args: `{"path":"c.go"}`},
	}
	changes := []Change{
		{Path: "a.go", Kind: Modified},
		{Path: "b.go", Kind: Modified},
		{Path: "c.go", Kind: Modified},
	}
	rep := Classify("/repo", changes, events)
	if rep.Changed != 3 {
		t.Fatalf("changed = %d", rep.Changed)
	}
	if rep.Covered != 2 || rep.Unrecorded != 1 || rep.Unverified != 0 {
		t.Fatalf("counts = covered %d unverified %d unrecorded %d", rep.Covered, rep.Unverified, rep.Unrecorded)
	}
	if rep.Files[0].Path != "b.go" || rep.Files[0].Status != Unrecorded {
		t.Fatalf("unrecorded should sort first: %+v", rep.Files)
	}
}

func TestClassifyShellMakesChangesUnverified(t *testing.T) {
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "run_command", Status: event.StatusPending, Args: `{"command":"go","args":["build"]}`},
	}
	rep := Classify("/repo", []Change{{Path: "bin/app", Kind: Modified}}, events)
	if rep.Unverified != 1 || rep.Unrecorded != 0 {
		t.Fatalf("expected unverified, got %+v", rep)
	}
}

func TestClassifyDeniedWritesDoNotCover(t *testing.T) {
	events := []event.Event{
		{Direction: event.ClientToServer, Method: "tools/call", Tool: "write_file", Status: event.StatusDenied, Args: `{"path":"a.go"}`},
	}
	rep := Classify("/repo", []Change{{Path: "a.go", Kind: Modified}}, events)
	if rep.Unrecorded != 1 {
		t.Fatalf("a denied write must not cover a change: %+v", rep)
	}
}
