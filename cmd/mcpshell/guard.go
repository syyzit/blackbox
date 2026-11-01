package main

import (
	"fmt"
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
	}
	return nil
}

var findActions = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true, "-delete": true,
	"-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true,
}

// checkPathArg rejects an argument (or a --flag=value) that names a path outside the
// root: an absolute path, or a relative one that climbs out with "..".
func checkPathArg(root, dir, arg string) error {
	v := arg
	if strings.HasPrefix(v, "-") {
		i := strings.IndexByte(v, '=')
		if i < 0 {
			return nil
		}
		v = v[i+1:]
	}
	if v == "" {
		return nil
	}
	var p string
	switch {
	case looksAbsolute(v):
		p = v
	case hasDotDot(v):
		p = filepath.Join(dir, v)
	default:
		return nil
	}
	abs, err := filepath.Abs(p)
	if err != nil || !within(root, abs) {
		return fmt.Errorf("argument %q points outside the repo root", arg)
	}
	return nil
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
		if a == "--output" || strings.HasPrefix(a, "--output=") || a == "--ext-diff" || a == "--textconv" {
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

// gitPrefix runs every git command with repository-configured programs switched off,
// since the agent can edit .git/config through the file tools.
func gitPrefix(sub string) []string {
	pre := []string{"-c", "core.fsmonitor=false"}
	switch sub {
	case "diff", "log", "show":
		return append(pre, sub, "--no-ext-diff", "--no-textconv")
	}
	return append(pre, sub)
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
