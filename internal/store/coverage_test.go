package store

import (
	"testing"

	"github.com/syyzit/blackbox/internal/coverage"
)

func TestCoverageRoundTrip(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	rep := coverage.Report{
		Root: "/repo", Scanned: 10, Changed: 2, Unrecorded: 1,
		Files: []coverage.Class{
			{Path: "b.go", Kind: coverage.Modified, Status: coverage.Unrecorded},
			{Path: "a.go", Kind: coverage.Modified, Status: coverage.Covered},
		},
	}
	if err := sess.SaveCoverage(rep); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := CoverageFor(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Unrecorded != 1 || len(got.Files) != 2 || got.Files[0].Path != "b.go" {
		t.Fatalf("bad coverage round trip: %+v", got)
	}

	bundle, err := Export(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Coverage == nil || bundle.Coverage.Changed != 2 {
		t.Fatalf("coverage missing from export: %+v", bundle.Coverage)
	}
}

func TestCoverageMissingIsNil(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := CoverageFor(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
