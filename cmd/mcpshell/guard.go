package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkArgs rejects arguments that would take a command outside what mcpshell
// promises: reading or writing outside the root, running other programs (find
// -exec), or changing repository state through a git subcommand that is read-only
// only in some of its forms.
//
// This narrows the obvious escapes; it is not a sandbox. go and make run code from
// the repository by design.
func checkArgs(root, dir, command string, args []string) error {
	if command != "echo" {
		for _, a := range args {
			if err := checkPathArg(root, dir, a); err != nil {
				return err
			}
		}
	}
	switch command {
	case "find":
		for _, a := range args {
			if findActions[a] {
				return fmt.Errorf("find %s is not allowed: it runs programs or changes files", a)
			}
		}
	case "git":
		return checkGit(args)
	case "rg":
		return checkRg(args)
	}
	return nil
}

// rgPrograms are ripgrep options that run a program: --pre for every file searched,
// --hostname-bin to name the host in hyperlinks. ripgrep does not accept abbreviated
// long options, so exact names suffice. They are refused anywhere in the arguments,
// even after "--": telling an option's value from an option would take rg's parser,
// and refusing a search for the literal text "--pre" costs little.
var rgPrograms = []string{"--pre", "--hostname-bin"}

func checkRg(args []string) error {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		for _, opt := range rgPrograms {
			if name == opt {
				return fmt.Errorf("rg %s is not allowed: it runs an external program", opt)
			}
		}
	}
	return nil
}

var findActions = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true, "-delete": true,
	"-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true,
}

// checkPathArg rejects an argument (or a --flag=value, or a short option's attached
// value) that names a path outside the root: an absolute path, a relative one that
// climbs out with "..", or one that leads out through a symlink.
func checkPathArg(root, dir, arg string) error {
	switch {
	case strings.HasPrefix(arg, "--"):
		i := strings.IndexByte(arg, '=')
		if i < 0 {
			return nil
		}
		return checkPathValue(root, dir, arg, arg[i+1:])
	case strings.HasPrefix(arg, "-"):
		// A Go-style flag: -o=<file>.
		if i := strings.IndexByte(arg, '='); i >= 0 {
			if err := checkPathValue(root, dir, arg, arg[i+1:]); err != nil {
				return err
			}
		}
		// A short option can carry its value attached (grep -f<file>, make -C<dir>),
		// after other flags bundled with it (-rf<file>). Which letters take a value
		// differs by command, so whatever follows each letter of the leading bundle is
		// checked as a value. The bundle ends at the first character that cannot be an
		// option letter, so go test -run=TestX/sub is not taken to name /sub.
		for i := 2; i < len(arg) && isOptionLetter(arg[i-1]); i++ {
			if err := checkPathValue(root, dir, arg, arg[i:]); err != nil {
				return err
			}
		}
		return nil
	}
	return checkPathValue(root, dir, arg, arg)
}

// isOptionLetter reports whether c can be a short option, or a digit of a numeric one
// (grep -5).
func isOptionLetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// checkPathValue checks v, which is arg or part of it, as a path.
func checkPathValue(root, dir, arg, v string) error {
	if v == "" {
		return nil
	}
	// raw is joined without cleaning, so a ".." after a symlink is resolved from where
	// the link leads, as the OS would, not lexically.
	raw := v
	if !looksAbsolute(v) {
		raw = dir + string(filepath.Separator) + v
	}
	if looksAbsolute(v) || hasDotDot(v) {
		abs, err := filepath.Abs(raw)
		if err != nil || !within(root, abs) {
			return fmt.Errorf("argument %q points outside the repo root", arg)
		}
	}
	// Any argument may name a path, and a symlink inside the root can lead out of it.
	if !realWithin(root, raw) {
		return fmt.Errorf("argument %q points outside the repo root", arg)
	}
	return nil
}

// realWithin reports whether p is inside root once the symlinks in both are resolved.
// Only the part of p that exists can be resolved; the rest is taken as written, since
// it cannot be a link yet.
func realWithin(root, p string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	return within(realRoot, realPath(p))
}

// realPath resolves the symlinks in the deepest existing ancestor of p. Any failure
// to resolve, not only a missing file, moves up a level: an argument that is a
// pattern rather than a path can be a name the OS rejects outright.
func realPath(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			abs, err := filepath.Abs(filepath.Join(r, rest))
			if err != nil {
				return ""
			}
			return abs
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// looksAbsolute also treats /x and \x as absolute on Windows, where filepath.IsAbs
// requires a drive letter but the OS would resolve them against the current drive.
func looksAbsolute(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || filepath.VolumeName(p) != ""
}

func hasDotDot(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func within(root, abs string) bool {
	return abs == root || strings.HasPrefix(abs, strings.TrimRight(root, `/\`)+string(filepath.Separator))
}

// checkGit allows the read-only forms of the allowlisted subcommands.
func checkGit(args []string) error {
	if len(args) == 0 || !gitSubcommands[args[0]] {
		return fmt.Errorf("git is limited to read-only subcommands")
	}
	sub, rest := args[0], args[1:]
	for _, a := range rest {
		if a == "--help" {
			// git turns this into `git help`, which starts whatever viewer the
			// config names (man.<tool>.path, browser.<tool>.cmd).
			return fmt.Errorf("git %s --help is not allowed: it runs an external viewer", sub)
		}
		if isForbiddenGitOption(a) {
			return fmt.Errorf("git %s %s is not allowed: it writes files or runs external programs", sub, a)
		}
	}
	switch sub {
	case "branch":
		return checkGitBranch(rest)
	case "tag":
		return checkGitTag(rest)
	case "remote":
		return checkGitRemote(rest)
	}
	return nil
}

// forbiddenGitOptions write files (--output) or run configured programs: external
// diff and textconv drivers, and the signature verifier (gpg.program).
var forbiddenGitOptions = []string{"--output", "--ext-diff", "--textconv", "--show-signature"}

// isForbiddenGitOption also catches abbreviations, which git accepts for long options
// (--ext, --outp=x). "--text" is an option of its own, not a short --textconv.
func isForbiddenGitOption(arg string) bool {
	name := arg
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	if len(name) < 3 || !strings.HasPrefix(name, "--") || name == "--text" {
		return false
	}
	for _, opt := range forbiddenGitOptions {
		if strings.HasPrefix(opt, name) {
			return true
		}
	}
	return false
}

// gitSafeConfig overrides, for one command, the settings that would make git run a
// program the repository chooses: the fsmonitor hook, hooks in general (git status
// rewrites the index and then runs post-index-change), and signature verification
// while listing commits. The agent can edit .git/config and .git/hooks through the
// file tools, and the command line wins over both.
func gitSafeConfig() []string {
	return []string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "log.showSignature=false",
	}
}

// commandEnv is the environment a command runs with; nil means mcpshell's own.
func commandEnv(command string) []string {
	switch command {
	case "git":
		return gitEnv()
	case "rg":
		// rg reads extra arguments from the file this names, which could hold --pre
		// and would never pass through checkRg.
		return withoutEnv(os.Environ(), "RIPGREP_CONFIG_PATH")
	}
	return nil
}

// withoutEnv drops a variable, matching its name without regard to case as Windows
// does.
func withoutEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, name) {
			out = append(out, kv)
		}
	}
	return out
}

// gitEnv keeps git from rewriting the index as a side effect of reading it.
func gitEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
}

// gitPrefix runs every git command with repository-configured programs switched off.
func gitPrefix(sub string) []string {
	pre := gitSafeConfig()
	switch sub {
	case "diff", "log", "show":
		return append(pre, sub, "--no-ext-diff", "--no-textconv")
	case "blame":
		return append(pre, sub, "--no-textconv") // blame applies textconv by default
	}
	return append(pre, sub)
}

// repoProgramKeys are the settings that name a program by a key the command line
// cannot override in advance, because the key contains a name of the repository's
// choosing (filter.<name>.clean), or that should keep working when the user set them
// (gpg.program). Names are as git prints them: lower case outside the subsection.
const repoProgramKeys = `^(filter\..*\.(clean|smudge|process)|diff\..*\.(textconv|command)|diff\.external|gpg\.(.*\.)?program)$`

// checkGitConfig refuses to run git in dir when the repository's own configuration
// (.git/config, a worktree's config, or a file either includes) defines a filter, a
// diff driver or a signature program. .gitattributes can route any file through such
// a driver, and status, diff and blame would then run it.
//
// Only the repository's scopes count: the user's and the system's configuration are
// not the agent's to edit, and often define a filter themselves (git-lfs).
func checkGitConfig(ctx context.Context, dir, sub string) error {
	args := append(gitSafeConfig(), "config", "--show-scope", "--includes", "--name-only", "--get-regexp", repoProgramKeys)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil // no such key in any scope
		}
		// Without an answer, assume the worst rather than run git unchecked.
		return fmt.Errorf("git %s is not allowed: the repository config could not be checked: %s", sub, firstLine(string(out), err.Error()))
	}
	for _, line := range strings.Split(string(out), "\n") {
		scope, key, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		if scope == "local" || scope == "worktree" {
			return fmt.Errorf("git %s is not allowed: the repository config sets %s, which names a program git would run", sub, key)
		}
	}
	return nil
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return fallback
	}
	return s
}

// git branch creates, renames or deletes branches unless it is listing. Listing is
// recognized by its flags; positional arguments are patterns only with --list.
func checkGitBranch(args []string) error {
	listing := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--list":
			listing = true
		case branchValueFlags[a]:
			i++ // the flag's value, e.g. --contains <commit>
		case strings.HasPrefix(a, "--"):
			name := a
			if j := strings.IndexByte(a, '='); j >= 0 {
				name = a[:j]
			}
			if !branchLongFlags[name] {
				return fmt.Errorf("git branch %s is not allowed: only listing is read-only", a)
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, c := range a[1:] {
				if !strings.ContainsRune("arvl", c) {
					return fmt.Errorf("git branch %s is not allowed: only listing is read-only", a)
				}
				if c == 'l' {
					listing = true
				}
			}
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 0 && !listing {
		return fmt.Errorf("git branch %s would create a branch; use --list for patterns", positional[0])
	}
	return nil
}

var branchValueFlags = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
}

var branchLongFlags = map[string]bool{
	"--all": true, "--remotes": true, "--verbose": true, "--show-current": true,
	"--sort": true, "--format": true, "--color": true, "--no-color": true,
	"--column": true, "--no-column": true, "--abbrev": true, "--no-abbrev": true,
	"--ignore-case": true, "--omit-empty": true,
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
}

// git tag creates or deletes tags unless it is listing.
func checkGitTag(args []string) error {
	if len(args) == 0 {
		return nil
	}
	listing := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-l" || a == "--list":
			listing = true
		case a == "--contains" || a == "--no-contains" || a == "--merged" || a == "--no-merged" || a == "--points-at":
			listing = true
			i++
		case strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--sort=") || strings.HasPrefix(a, "--format=") ||
			a == "--column" || a == "--no-column" || a == "-i" || a == "--ignore-case":
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("git tag %s is not allowed: only listing is read-only", a)
		}
	}
	if !listing {
		return fmt.Errorf("git tag with a name would create a tag; use -l for patterns")
	}
	return nil
}

// git remote only lists or reports URLs.
func checkGitRemote(args []string) error {
	switch {
	case len(args) == 0:
		return nil
	case len(args) == 1 && (args[0] == "-v" || args[0] == "--verbose"):
		return nil
	case args[0] == "get-url":
		return nil
	}
	return fmt.Errorf("git remote %s is not allowed: only listing is read-only", strings.Join(args, " "))
}
