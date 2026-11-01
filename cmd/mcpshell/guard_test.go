package main

import (
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
		{"log", "--oneline", "-5"},
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
}
