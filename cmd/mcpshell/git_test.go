package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests build a real repository whose configuration names a hostile program,
// the way an agent with file tools could arrange it, and check that no read-only git
// command mcpshell allows ends up running that program.
//
// The hostile program has to exist on every OS, so it is a tiny Go program built once
// per test run. It leaves a PWNED file next to itself and copies stdin to stdout, so
// it also works as a filter.
//
// The timeout test uses the same program: "spawn" starts a copy of itself that
// inherits the output pipe, and both then linger until a STOP file appears.

const hostileSource = `package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	dir := filepath.Dir(exe)
	if len(os.Args) > 1 && (os.Args[1] == "spawn" || os.Args[1] == "linger") {
		if os.Args[1] == "spawn" {
			child := exec.Command(exe, "linger")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(3)
			}
			fmt.Println("spawned")
		}
		for i := 0; i < 300; i++ {
			if _, err := os.Stat(filepath.Join(dir, "STOP")); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "PWNED"), nil, 0o600); err != nil {
		os.Exit(3)
	}
	io.Copy(os.Stdout, os.Stdin)
}
`

var hostile struct {
	once sync.Once
	dir  string
	exe  string
	err  error
}

// hostileProgram returns the program's path, with forward slashes so it survives the
// shell git runs configured commands with, and the marker file it creates.
func hostileProgram(t *testing.T) (exe, marker string) {
	t.Helper()
	hostile.once.Do(func() {
		dir, err := os.MkdirTemp("", "mcpshell-hostile")
		if err != nil {
			hostile.err = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(hostileSource), 0o600); err != nil {
			hostile.err = err
			return
		}
		exe := filepath.Join(dir, "hostile.exe")
		if out, err := exec.Command("go", "build", "-o", exe, src).CombinedOutput(); err != nil {
			hostile.err = &exec.Error{Name: "go build: " + string(out), Err: err}
			return
		}
		hostile.dir, hostile.exe = dir, filepath.ToSlash(exe)
	})
	if hostile.err != nil {
		t.Fatalf("building the hostile program: %v", hostile.err)
	}
	marker = filepath.Join(hostile.dir, "PWNED")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return hostile.exe, marker
}

func TestMain(m *testing.M) {
	code := m.Run()
	if hostile.dir != "" {
		os.RemoveAll(hostile.dir)
	}
	os.Exit(code)
}

// gitRepo creates a repository with one commit and returns a server rooted in it. The
// user's own git configuration is kept out, so the test sees only what it sets up.
func gitRepo(t *testing.T) (*server, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	repo := t.TempDir()
	// Resolve symlinks (macOS /var) so the root matches the paths git reports.
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		repo = real
	}
	git(t, repo, "init", "--quiet")
	git(t, repo, "config", "user.name", "Test")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "core.autocrlf", "false")
	writeFile(t, repo, "a.txt", "one\n")
	git(t, repo, "add", "a.txt")
	git(t, repo, "commit", "--quiet", "-m", "first")

	return &server{
		root:      repo,
		allow:     splitList("git,go"),
		timeout:   30 * time.Second,
		maxOutput: 16 << 10,
	}, repo
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// expectRefused runs each command and requires that it was refused naming the
// configuration key, and that the hostile program did not run.
func expectRefused(t *testing.T, s *server, marker, key string, commands [][]string) {
	t.Helper()
	for _, args := range commands {
		text, failed := s.execute(callArgs{Command: "git", Args: args})
		if exists(marker) {
			os.Remove(marker)
			t.Errorf("git %v ran the configured program:\n%s", args, text)
			continue
		}
		if !failed || !strings.Contains(text, key) || !strings.Contains(text, "not allowed") {
			t.Errorf("git %v: want a refusal naming %s, got failed=%v:\n%s", args, key, failed, text)
		}
	}
}

func TestGitRefusesRepoConfiguredFilter(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	writeFile(t, repo, ".gitattributes", "*.txt filter=x diff=x\n")
	git(t, repo, "config", "filter.x.clean", `"`+exe+`"`)
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	expectRefused(t, s, marker, "filter.x.clean", [][]string{
		{"status"},
		{"diff"},
		{"ls-files", "-m"},
		{"describe", "--always", "--dirty"},
		{"blame", "a.txt"},
	})
}

func TestGitRefusesRepoConfiguredTextconv(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	writeFile(t, repo, ".gitattributes", "*.txt diff=x\n")
	git(t, repo, "config", "diff.x.textconv", `"`+exe+`"`)
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	expectRefused(t, s, marker, "diff.x.textconv", [][]string{
		{"blame", "a.txt"},
		{"diff"},
		{"log", "-p"},
		{"show", "HEAD"},
	})
}

func TestGitRefusesRepoConfiguredDiffCommand(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	writeFile(t, repo, ".gitattributes", "*.txt diff=x\n")
	git(t, repo, "config", "diff.x.command", `"`+exe+`"`)
	git(t, repo, "config", "diff.external", `"`+exe+`"`)
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	expectRefused(t, s, marker, "diff.", [][]string{{"diff"}, {"log", "-p"}, {"status", "-v"}})
}

// A driver defined in a file the repository config includes is just as hostile.
func TestGitRefusesIncludedConfig(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	writeFile(t, repo, ".gitattributes", "*.txt filter=x\n")
	writeFile(t, repo, "tools/git.cfg", "[filter \"x\"]\n\tclean = \\\""+exe+"\\\"\n")
	git(t, repo, "config", "include.path", "../tools/git.cfg")
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	expectRefused(t, s, marker, "filter.x.clean", [][]string{{"status"}, {"diff"}})
}

func TestGitRefusesRepoConfiguredSignatureProgram(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	git(t, repo, "config", "gpg.program", exe)
	git(t, repo, "config", "log.showSignature", "true")

	// A commit with a signature header, so there is something to verify.
	tree := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD^{tree}"))
	parent := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	commit := "tree " + tree + "\nparent " + parent + "\n" +
		"author Test <test@example.com> 1700000000 +0000\n" +
		"committer Test <test@example.com> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----\n" +
		"\nsigned\n"
	hash := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	hash.Dir = repo
	hash.Stdin = strings.NewReader(commit)
	out, err := hash.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	git(t, repo, "update-ref", "HEAD", strings.TrimSpace(string(out)))

	expectRefused(t, s, marker, "gpg.program", [][]string{
		{"log"},
		{"log", "--format=%G?"},
		{"show", "HEAD"},
	})
}

// git status rewrites the index when it refreshes it, and git runs the
// post-index-change hook after that. Hooks live in .git, which the agent can write.
func TestGitStatusDoesNotRunHooks(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	writeFile(t, repo, ".git/hooks/post-index-change", "#!/bin/sh\n\""+exe+"\"\n")
	// Same content, new timestamp: the index entry is stale and gets refreshed.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}

	text, failed := s.execute(callArgs{Command: "git", Args: []string{"status"}})
	if exists(marker) {
		t.Fatalf("git status ran the post-index-change hook:\n%s", text)
	}
	if failed {
		t.Fatalf("git status failed:\n%s", text)
	}
}

// The fsmonitor setting names a program too; it is overridden rather than refused.
func TestGitDoesNotRunFsmonitor(t *testing.T) {
	s, repo := gitRepo(t)
	exe, marker := hostileProgram(t)
	git(t, repo, "config", "core.fsmonitor", `"`+exe+`"`)
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	for _, args := range [][]string{{"status"}, {"diff"}, {"ls-files", "-m"}} {
		text, failed := s.execute(callArgs{Command: "git", Args: args})
		if exists(marker) {
			os.Remove(marker)
			t.Errorf("git %v ran the fsmonitor program:\n%s", args, text)
		}
		if failed {
			t.Errorf("git %v failed:\n%s", args, text)
		}
	}
}

// The user's own configuration is not the agent's to edit, so a filter defined there
// (git-lfs installs one) must not lock git out.
func TestGitAllowsUserConfiguredFilter(t *testing.T) {
	s, repo := gitRepo(t)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.WriteFile(global, []byte("[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tprocess = git-lfs filter-process\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "a.txt", "one\ntwo\n")

	for _, args := range [][]string{{"status", "--short"}, {"diff", "--stat"}, {"log", "--oneline"}, {"blame", "a.txt"}} {
		text, failed := s.execute(callArgs{Command: "git", Args: args})
		if failed {
			t.Errorf("git %v was refused or failed:\n%s", args, text)
		}
	}
	if text, _ := s.execute(callArgs{Command: "git", Args: []string{"status", "--short"}}); !strings.Contains(text, "a.txt") {
		t.Errorf("git status did not report the change:\n%s", text)
	}
}

// Killing a command at its deadline does not kill what it started. A child that
// inherited the output pipe used to keep execute waiting until it exited on its own.
func TestExecuteTimeoutBoundsCommandThatLeavesAChild(t *testing.T) {
	exe, _ := hostileProgram(t)
	s := testServer(t)
	s.allow[exe] = true
	stop := filepath.Join(filepath.Dir(exe), "STOP")
	// Registered after the server's temporary directory, so it runs before that is
	// removed: the lingering processes have it as their working directory.
	t.Cleanup(func() {
		os.WriteFile(stop, nil, 0o600)
		time.Sleep(500 * time.Millisecond)
		os.Remove(stop)
	})
	start := time.Now()
	text, failed := s.execute(callArgs{Command: exe, Args: []string{"spawn"}, TimeoutMS: 1000})
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("a 1s timeout took %s", elapsed.Round(time.Millisecond))
	}
	if !failed || !strings.Contains(text, "command timed out") || !strings.Contains(text, "exit -1") {
		t.Errorf("the timeout was not reported as one, failed=%v:\n%s", failed, text)
	}
	if !strings.Contains(text, "spawned") {
		t.Errorf("output written before the timeout was lost:\n%s", text)
	}
}

func TestGitWorksOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	s := testServer(t)
	text, failed := s.execute(callArgs{Command: "git", Args: []string{"status"}})
	// git's own complaint, not a refusal or a crash of the config check.
	if !failed || !strings.Contains(strings.ToLower(text), "not a git repository") {
		t.Fatalf("failed=%v:\n%s", failed, text)
	}
}
