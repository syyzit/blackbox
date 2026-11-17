package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// init inserts os.Executable(), which on Windows is a backslashed path ending in
// blackbox.exe. The check must recognise it on every OS, since a config written on
// one machine is read wherever the file is synced.
func TestIsWrappedRecognisesEveryFormOfTheBinary(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"blackbox", true},
		{"/usr/local/bin/blackbox", true},
		{"blackbox.exe", true},
		{`C:\Users\me\bin\blackbox.exe`, true},
		{`C:\Users\me\bin\BlackBox.EXE`, true},
		{"C:/Users/me/bin/blackbox.exe", true},
		{`C:\tools\blackbox`, true},
		{"notblackbox", false},
		{`C:\blackbox\npx.exe`, false},
		{"blackbox.exe.bak", false},
		{"npx", false},
	}
	for _, tc := range cases {
		if got := isWrapped([]string{tc.command, "run", "--", "npx", "srv"}); got != tc.want {
			t.Errorf("isWrapped(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
	if isWrapped([]string{"blackbox", "ui"}) || isWrapped([]string{"blackbox", "run", "npx"}) {
		t.Error("a blackbox command that is not `run --` counts as wrapped")
	}
}

func TestWrapTwiceWithWindowsBinaryDoesNotNest(t *testing.T) {
	const exe = `C:\Program Files\Black Box\blackbox.exe`
	for _, style := range []Style{StyleMcpServers, StyleOpenCode} {
		path := filepath.Join(t.TempDir(), "mcp.json")
		body := `{"mcpServers":{"fs":{"command":"npx","args":["srv"]}}}`
		if style == StyleOpenCode {
			body = `{"mcp":{"fs":{"type":"local","command":["npx","srv"]}}}`
		}
		write(t, path, body)

		cfg, err := Load(Location{Path: path, Style: style})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := cfg.WrapWith("fs", exe); err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.Write(); err != nil {
			t.Fatal(err)
		}

		again, err := Load(Location{Path: path, Style: style})
		if err != nil {
			t.Fatal(err)
		}
		servers, err := again.Servers()
		if err != nil {
			t.Fatal(err)
		}
		if !servers[0].Wrapped {
			t.Fatalf("style %d: wrapped entry not recognised: %v", style, servers[0].Command)
		}
		if _, _, err := again.WrapWith("fs", exe); err == nil {
			t.Fatalf("style %d: wrapped twice: %s", style, again.Bytes())
		}
	}
}

// Server names are arbitrary JSON keys; every character sjson gives a meaning to has
// to reach it escaped, or the edit lands somewhere else or nowhere.
func TestWrapServerNamesWithPathSyntax(t *testing.T) {
	names := []string{
		":x", "a|b", "a#b", "#", "@x", "@this", "!x", "a.b", "a*b", "a?b", `a\b`,
		"-1", "0", "a b", "[x]", "{x}", "a:b", "a=b", "a>b", "a%b", "naïve", `a"b`, "",
	}
	for _, style := range []Style{StyleMcpServers, StyleOpenCode} {
		for _, name := range names {
			entries := map[string]any{
				"other": map[string]any{"command": "node", "args": []string{"other.js"}},
				name:    map[string]any{"command": "npx", "args": []string{"srv"}},
			}
			if style == StyleOpenCode {
				entries = map[string]any{
					"other": map[string]any{"command": []string{"node", "other.js"}},
					name:    map[string]any{"command": []string{"npx", "srv"}},
				}
			}
			body, err := json.Marshal(map[string]any{style.Key(): entries, "keep": true})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "mcp.json")
			write(t, path, string(body))

			cfg, err := Load(Location{Path: path, Style: style})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := cfg.Wrap(name); err != nil {
				t.Errorf("style %d: Wrap(%q): %v", style, name, err)
				continue
			}

			var root map[string]map[string]json.RawMessage
			var rest map[string]json.RawMessage
			if err := json.Unmarshal(cfg.Bytes(), &rest); err != nil {
				t.Errorf("style %d: Wrap(%q) broke the JSON: %v\n%s", style, name, err, cfg.Bytes())
				continue
			}
			delete(rest, "keep")
			b, _ := json.Marshal(rest)
			if err := json.Unmarshal(b, &root); err != nil {
				t.Fatal(err)
			}
			if got := len(root[style.Key()]); got != 2 {
				t.Errorf("style %d: Wrap(%q) left %d entries, want 2: %s", style, name, got, cfg.Bytes())
				continue
			}
			servers, err := cfg.Servers()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range servers {
				want := []string{"blackbox", "run", "--", "npx", "srv"}
				if s.Name == "other" {
					want = []string{"node", "other.js"}
				}
				if !reflect.DeepEqual(s.Command, want) {
					t.Errorf("style %d: after Wrap(%q), %q runs %v, want %v", style, name, s.Name, s.Command, want)
				}
			}
		}
	}
}

// Arguments that are not strings are the user's to fix or keep; wrapping must not
// turn them into empty strings.
func TestWrapPreservesNonStringArgs(t *testing.T) {
	cases := []struct {
		name  string
		style Style
		body  string
		want  string
	}{
		{"args", StyleMcpServers,
			`{"mcpServers":{"fs":{"command":"srv","args":["--port",8080,true,null,"a\/b"]}}}`,
			`{"mcpServers":{"fs":{"command":"blackbox","args":["run","--","srv","--port",8080,true,null,"a\/b"]}}}`},
		{"opencode command", StyleOpenCode,
			`{"mcp":{"fs":{"command":["srv","--port",8080]}}}`,
			`{"mcp":{"fs":{"command":["blackbox","run","--","srv","--port",8080]}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.json")
			write(t, path, tc.body)
			cfg, err := Load(Location{Path: path, Style: tc.style})
			if err != nil {
				t.Fatal(err)
			}
			before, after, err := cfg.Wrap("fs")
			if err != nil {
				t.Fatal(err)
			}
			if string(cfg.Bytes()) != tc.want {
				t.Fatalf("rewritten:\n%s\nwant:\n%s", cfg.Bytes(), tc.want)
			}
			if !strings.Contains(before, "8080") || !strings.Contains(after, "8080") {
				t.Fatalf("display lost the argument: %q -> %q", before, after)
			}
		})
	}
}

func TestWriteNeverOverwritesABackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	const pristine = `{"mcpServers":{"a":{"command":"npx","args":["a"]},"b":{"command":"npx","args":["b"]}}}`
	write(t, path, pristine)

	cfg, err := Load(Location{Path: path, Style: StyleMcpServers})
	if err != nil {
		t.Fatal(err)
	}
	backups := map[string]bool{}
	// Two writes well inside one second: each needs its own backup file.
	for _, name := range []string{"a", "b"} {
		if _, _, err := cfg.Wrap(name); err != nil {
			t.Fatal(err)
		}
		backup, err := cfg.Write()
		if err != nil {
			t.Fatal(err)
		}
		if backups[backup] {
			t.Fatalf("backup %s was written twice", backup)
		}
		backups[backup] = true
	}

	found := false
	for backup := range backups {
		b, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		found = found || string(b) == pristine
	}
	if !found {
		t.Fatal("no backup holds the original file any more")
	}

	// Nothing but the config and its backups is left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1+len(backups) {
		t.Fatalf("directory holds %d files, want %d", len(entries), 1+len(backups))
	}
}

func TestWriteFileAtomicKeepsModeAndFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "settings.json")
	write(t, real, "old")

	if err := WriteFileAtomic(real, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(real); string(b) != "new" {
		t.Fatalf("content = %q", b)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(real)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", info.Mode().Perm())
		}
	}

	// A config kept in a dotfiles repository is a symlink; it must stay one.
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := WriteFileAtomic(link, []byte("through link"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced by a file: %v, %v", info, err)
	}
	if b, _ := os.ReadFile(real); string(b) != "through link" {
		t.Fatalf("target content = %q", b)
	}
}
