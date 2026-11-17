package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckArgsKeepsPathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sub")

	denied := [][]string{
		{"cat", "/etc/passwd"},
		{"cat", `\Windows\win.ini`},
		{"cat", "../../outside.txt"},
		{"grep", "-r", "x", "--include=../../*"},
		{"git", "diff", "--no-index", "/etc/hosts", "x"},
	}
	for _, c := range denied {
		if err := checkArgs(root, dir, c[0], c[1:]); err == nil || !strings.Contains(err.Error(), "outside the repo root") {
			t.Errorf("%v: got %v, want outside-root rejection", c, err)
		}
	}

	allowed := [][]string{
		{"cat", "README.md"},
		{"cat", "../README.md"}, // from sub/ back to the root is fine
		{"grep", "-rn", "needle", "."},
		{"git", "log", "HEAD~3..HEAD"},
		{"echo", "/etc/passwd"}, // echo reads nothing
		{"cat", filepath.Join(root, "go.mod")},
	}
	for _, c := range allowed {
		if err := checkArgs(root, dir, c[0], c[1:]); err != nil {
			t.Errorf("%v: unexpected rejection %v", c, err)
		}
	}
}

// A short option takes its value attached as well as in the next argument: grep
// -f/etc/passwd reads its patterns from that file, git diff -O<file> its order file,
// make -C<dir> changes directory first. Options can be bundled (-rf<file>).
func TestCheckArgsChecksAttachedShortOptionValues(t *testing.T) {
	root := t.TempDir()
	denied := [][]string{
		{"grep", "-f/etc/passwd", "x", "."},
		{"grep", "-f../../outside.txt", "."},
		{"grep", "-rf/etc/passwd", "."},
		{"grep", `-f\Windows\win.ini`, "."},
		{"rg", "-f/etc/passwd", "."},
		{"git", "diff", "-O/etc/passwd"},
		{"make", "-C/tmp"},
		{"make", "-f../../Makefile"},
	}
	for _, c := range denied {
		if err := checkArgs(root, root, c[0], c[1:]); err == nil || !strings.Contains(err.Error(), "outside the repo root") {
			t.Errorf("%v: got %v, want outside-root rejection", c, err)
		}
	}
	allowed := [][]string{
		{"grep", "-rn", "needle", "."},
		{"grep", "-A3", "-B2", "needle", "."},
		{"grep", "-fpatterns.txt", "."},
		{"head", "-n5", "README.md"},
		{"git", "log", "-5"},
		{"go", "test", "-run=TestX/sub", "./..."}, // a Go flag, not a bundle
		{"go", "test", "-coverprofile=out/c.txt", "./..."},
		{"grep", "-e", "a/b", "."},
	}
	for _, c := range allowed {
		if err := checkArgs(root, root, c[0], c[1:]); err != nil {
			t.Errorf("%v: unexpected rejection %v", c, err)
		}
	}
}

func TestCheckArgsBlocksFindActions(t *testing.T) {
	root := t.TempDir()
	for _, a := range []string{"-exec", "-execdir", "-delete", "-fprint"} {
		if err := checkArgs(root, root, "find", []string{".", a, "x"}); err == nil {
			t.Errorf("find %s was allowed", a)
		}
	}
	if err := checkArgs(root, root, "find", []string{".", "-name", "*.go"}); err != nil {
		t.Errorf("plain find rejected: %v", err)
	}
}

// rg can run a program per file (--pre) or to find the host name (--hostname-bin), and
// reads extra arguments from the file RIPGREP_CONFIG_PATH names, which could say the
// same. The guard is checked directly: rg need not be installed to test it.
func TestRgCannotRunPrograms(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--pre", "./x", "needle"},
		{"--pre=./x", "needle"},
		{"needle", ".", "--pre", "x"},
		{"--hostname-bin", "./x", "needle"},
		{"--hostname-bin=./x", "needle"},
	} {
		if err := checkArgs(root, root, "rg", args); err == nil {
			t.Errorf("rg %v was allowed", args)
		}
	}
	for _, args := range [][]string{
		{"needle"},
		{"-n", "--pre-glob", "*.pdf", "needle"}, // only says which files --pre would see
		{"--pretty", "needle"},
	} {
		if err := checkArgs(root, root, "rg", args); err != nil {
			t.Errorf("rg %v rejected: %v", args, err)
		}
	}

	t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(root, "rgconfig"))
	env := commandEnv("rg")
	if env == nil {
		env = os.Environ() // nil runs the command with mcpshell's own environment
	}
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "RIPGREP_CONFIG_PATH=") {
			t.Fatalf("rg would read %s", kv)
		}
	}
}

func TestCheckGitReadOnlyForms(t *testing.T) {
	denied := [][]string{
		{"push"},
		{"branch", "new-branch"},
		{"branch", "-D", "main"},
		{"branch", "-vd", "main"},
		{"branch", "--delete", "main"},
		{"branch", "-m", "a", "b"},
		{"branch", "--set-upstream-to=origin/x"},
		{"tag", "v9"},
		{"tag", "-d", "v1"},
		{"tag", "-a", "v2", "-m", "x"},
		{"remote", "add", "evil", "https://example.com"},
		{"remote", "set-url", "origin", "https://example.com"},
		{"remote", "remove", "origin"},
		{"diff", "--output=/tmp/x"},
		{"log", "--output", "x"},
		{"diff", "--ext-diff"},
		{"show", "--textconv"},
		// git accepts unambiguous abbreviations of long options.
		{"diff", "--ext"},
		{"diff", "--outp=x"},
		{"show", "--textc"},
		// Both start a program the repository config can name.
		{"status", "--help"},
		{"log", "-1", "--help"},
		{"log", "--show-signature"},
		{"log", "--show-sig"},
	}
	for _, c := range denied {
		if err := checkGit(c); err == nil {
			t.Errorf("git %v was allowed", c)
		}
	}

	allowed := [][]string{
		{"status"},
		{"branch"},
		{"branch", "-a"},
		{"branch", "-vv"},
		{"branch", "--show-current"},
		{"branch", "--list", "feat/*"},
		{"branch", "--contains", "HEAD"},
		{"tag"},
		{"tag", "-l", "v*"},
		{"tag", "--points-at", "HEAD"},
		{"remote"},
		{"remote", "-v"},
		{"remote", "get-url", "origin"},
		{"diff", "--stat"},
		{"diff", "--text"}, // an option of its own, not an abbreviated --textconv
		{"log", "--oneline", "-5"},
		{"log", "--show-notes"},
	}
	for _, c := range allowed {
		if err := checkGit(c); err != nil {
			t.Errorf("git %v rejected: %v", c, err)
		}
	}
}

func TestGitPrefixDisablesRepoConfiguredPrograms(t *testing.T) {
	got := strings.Join(gitPrefix("diff"), " ")
	for _, want := range []string{"core.fsmonitor=false", "diff", "--no-ext-diff", "--no-textconv"} {
		if !strings.Contains(got, want) {
			t.Errorf("git diff prefix %q is missing %q", got, want)
		}
	}
	// blame applies textconv unless told not to.
	if got := strings.Join(gitPrefix("blame"), " "); !strings.Contains(got, "blame --no-textconv") {
		t.Errorf("git blame prefix %q is missing --no-textconv", got)
	}
	for _, sub := range []string{"status", "log", "blame"} {
		got := strings.Join(gitPrefix(sub), " ")
		for _, want := range []string{"core.fsmonitor=false", "core.hooksPath=", "log.showSignature=false"} {
			if !strings.Contains(got, want) {
				t.Errorf("git %s prefix %q is missing %q", sub, got, want)
			}
		}
	}
}

func FuzzCheckArgs(f *testing.F) {
	f.Add("git", "branch", "-vd")
	f.Add("cat", "../../x", "--y=/etc")
	f.Add("find", ".", "-exec")
	f.Add("git", "tag", "--contains")
	root := f.TempDir()
	f.Fuzz(func(t *testing.T, cmd, a, b string) {
		_ = checkArgs(root, root, cmd, []string{a, b})
		_ = checkArgs(root, root, cmd, []string{a})
		_ = checkArgs(root, root, cmd, nil)
	})
}
