package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spacecr8ed/blackbox/internal/coverage"
	"github.com/spacecr8ed/blackbox/internal/store"
)

func snapshotCoverage(root string) *coverage.Snapshot {
	if root == "" {
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	snap, err := coverage.Take(coverage.Options{Root: abs})
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · coverage disabled: %v\n", err)
		return nil
	}
	return snap
}

func finishCoverage(sess *store.Session, before *coverage.Snapshot) {
	if before == nil {
		return
	}
	after, err := coverage.Take(coverage.Options{Root: before.Root})
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · coverage: %v\n", err)
		return
	}
	_, events, err := store.Find(sess.Home, sess.Meta.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · coverage: %v\n", err)
		return
	}

	rep := coverage.Classify(before.Root, coverage.Diff(before, after), events)
	rep.Scanned = after.Scanned
	rep.Truncated = before.Truncated || after.Truncated
	if err := sess.SaveCoverage(rep); err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · coverage: %v\n", err)
		return
	}

	if rep.Unrecorded == 0 && rep.Unverified == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nblackbox · coverage: %d changed, %d not seen in a recorded write\n",
		rep.Changed, rep.Unrecorded)
	if rep.Truncated {
		fmt.Fprintln(os.Stderr, "blackbox · coverage: snapshot was truncated; the check is partial")
	}

	shown := 0
	for _, f := range rep.Files {
		if f.Status == coverage.Covered {
			continue
		}
		if shown >= 8 {
			fmt.Fprintf(os.Stderr, "  … %d more\n", len(rep.Files)-shown+rep.More)
			break
		}
		fmt.Fprintf(os.Stderr, "  %-10s %s (%s)\n", f.Status, f.Path, f.Kind)
		shown++
	}
}

func cmdCoverage(args []string) int {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox coverage: missing session id")
		return 2
	}

	rep, err := store.CoverageFor(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if rep == nil {
		fmt.Println("no coverage report for this session")
		return 0
	}

	fmt.Printf("root       %s\n", rep.Root)
	fmt.Printf("scanned    %d files\n", rep.Scanned)
	fmt.Printf("changed    %d · covered %d · unverified %d · unrecorded %d\n",
		rep.Changed, rep.Covered, rep.Unverified, rep.Unrecorded)
	if rep.Truncated {
		fmt.Println("note       snapshot was truncated; results are partial")
	}
	if len(rep.Files) == 0 {
		fmt.Println("\nnothing changed during this session")
		return 0
	}
	if rep.Unrecorded == 0 && rep.Unverified == 0 {
		fmt.Println("\nevery change is explained by a recorded call")
		return 0
	}

	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tCHANGE\tPATH")
	for _, f := range rep.Files {
		if f.Status == coverage.Covered {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Status, f.Kind, f.Path)
	}
	if rep.More > 0 {
		fmt.Fprintf(w, "…\t\t%d more\n", rep.More)
	}
	return flush(w)
}
