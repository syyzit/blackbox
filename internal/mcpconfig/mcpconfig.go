// Package mcpconfig discovers and rewrites MCP server entries in agent config files.
//
// Edits are applied with sjson so the rest of the user's file — key order, formatting,
// unrelated settings — is preserved byte-for-byte.
package mcpconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/sjson"
)

// Style is the shape of a client's MCP config.
type Style int

const (
	// StyleMcpServers is the Claude / Cursor shape: command + args.
	StyleMcpServers Style = iota
	// StyleServers is the VS Code shape: command + args under "servers".
	StyleServers
	// StyleOpenCode is the OpenCode shape: command is an array under "mcp".
	StyleOpenCode
)

// Key returns the top-level object holding the servers.
func (s Style) Key() string {
	switch s {
	case StyleServers:
		return "servers"
	case StyleOpenCode:
		return "mcp"
	default:
		return "mcpServers"
	}
}

// Location is a config file Black Box knows about.
type Location struct {
	Path   string
	Client string
	Style  Style
}

// Locations returns the config files worth checking for a working directory.
func Locations(workdir string) []Location {
	home, _ := os.UserHomeDir()
	locs := []Location{
		{filepath.Join(workdir, ".mcp.json"), "Claude Code", StyleMcpServers},
		{filepath.Join(workdir, ".cursor", "mcp.json"), "Cursor", StyleMcpServers},
		{filepath.Join(workdir, ".vscode", "mcp.json"), "VS Code", StyleServers},
		{filepath.Join(workdir, "opencode.json"), "OpenCode", StyleOpenCode},
		{filepath.Join(home, ".claude.json"), "Claude Code", StyleMcpServers},
		{filepath.Join(home, ".cursor", "mcp.json"), "Cursor", StyleMcpServers},
		{filepath.Join(home, ".config", "opencode", "opencode.json"), "OpenCode", StyleOpenCode},
	}
	switch runtime.GOOS {
	case "darwin":
		locs = append(locs, Location{
			Path:   filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
			Client: "Claude Desktop",
			Style:  StyleMcpServers,
		})
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			locs = append(locs, Location{
				Path:   filepath.Join(appdata, "Claude", "claude_desktop_config.json"),
				Client: "Claude Desktop",
				Style:  StyleMcpServers,
			})
		}
	default:
		locs = append(locs, Location{
			Path:   filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
			Client: "Claude Desktop",
			Style:  StyleMcpServers,
		})
	}
	return dedupe(locs)
}

func dedupe(locs []Location) []Location {
	seen := map[string]bool{}
	out := make([]Location, 0, len(locs))
	for _, loc := range locs {
		if seen[loc.Path] {
			continue
		}
		seen[loc.Path] = true
		out = append(out, loc)
	}
	return out
}

// Server is a discovered MCP server entry.
type Server struct {
	Name    string
	Command []string
	Remote  bool
	Wrapped bool
	Type    string

	// raw holds the command's elements as they appear in the file, so a rewrite can
	// put back one that is not a string (a port written as a number) unchanged.
	raw []json.RawMessage
}

// Config is one editable config file.
type Config struct {
	Path   string
	Client string
	Style  Style

	original []byte
	raw      []byte
	changed  bool
}

// Load reads a location. Missing files are reported by the caller.
func Load(loc Location) (*Config, error) {
	b, err := os.ReadFile(loc.Path)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("%s is not plain JSON (comments are not supported): %w", loc.Path, err)
	}
	return &Config{
		Path:     loc.Path,
		Client:   loc.Client,
		Style:    loc.Style,
		original: append([]byte(nil), b...),
		raw:      append([]byte(nil), b...),
	}, nil
}

// DetectStyle guesses the shape from the top-level keys.
func DetectStyle(raw []byte) (Style, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return StyleMcpServers, fmt.Errorf("not plain JSON: %w", err)
	}
	if _, ok := root["mcpServers"]; ok {
		return StyleMcpServers, nil
	}
	if _, ok := root["servers"]; ok {
		return StyleServers, nil
	}
	if _, ok := root["mcp"]; ok {
		return StyleOpenCode, nil
	}
	return StyleMcpServers, nil
}

// Servers lists the entries, sorted by name.
func (c *Config) Servers() ([]Server, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(c.raw, &root); err != nil {
		return nil, err
	}
	container, ok := root[c.Style.Key()]
	if !ok || len(container) == 0 {
		return nil, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(container, &entries); err != nil {
		return nil, fmt.Errorf("%s.%s is not an object", c.Path, c.Style.Key())
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Server, 0, len(names))
	for _, name := range names {
		// Fields are read one by one: a surprise in one of them must not hide the
		// others, as decoding the whole entry into a struct would.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(entries[name], &fields)
		var url, typ string
		_ = json.Unmarshal(fields["url"], &url)
		_ = json.Unmarshal(fields["type"], &typ)

		var raw []json.RawMessage
		if c.Style == StyleOpenCode {
			_ = json.Unmarshal(fields["command"], &raw)
		} else {
			var command string
			if json.Unmarshal(fields["command"], &command) == nil && command != "" {
				var args []json.RawMessage
				_ = json.Unmarshal(fields["args"], &args)
				raw = append([]json.RawMessage{fields["command"]}, args...)
			}
		}
		cmd := make([]string, 0, len(raw))
		for _, element := range raw {
			cmd = append(cmd, displayArg(element))
		}
		if len(cmd) == 0 {
			cmd = nil
		}

		remote := url != "" || typ == "remote" || typ == "sse" || typ == "http"
		out = append(out, Server{
			Name:    name,
			Command: cmd,
			Remote:  remote,
			Wrapped: isWrapped(cmd),
			Type:    typ,
			raw:     raw,
		})
	}
	return out, nil
}

// displayArg is a command element as text: a string's value, anything else as written.
func displayArg(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// isWrapped reports whether a command already runs through Black Box.
//
// The binary is compared by name alone, without regard to case, directory or a .exe
// suffix: init writes the path of the running executable, which on Windows is
// C:\...\blackbox.exe. Backslashes are split by hand because filepath.Base only
// knows the separator of the OS it runs on, and a config can be read on another.
func isWrapped(cmd []string) bool {
	if len(cmd) < 3 || cmd[1] != "run" || cmd[2] != "--" {
		return false
	}
	name := strings.ReplaceAll(cmd[0], `\`, "/")
	name = strings.ToLower(name[strings.LastIndex(name, "/")+1:])
	return strings.TrimSuffix(name, ".exe") == "blackbox"
}

// Wrap rewrites one server entry to run through Black Box.
// It returns the command before and after for display.
func (c *Config) Wrap(name string) (string, string, error) {
	return c.WrapWith(name, "blackbox")
}

// WrapWith is Wrap with an explicit binary path or name.
func (c *Config) WrapWith(name, command string) (string, string, error) {
	if command == "" {
		command = "blackbox"
	}
	servers, err := c.Servers()
	if err != nil {
		return "", "", err
	}
	var target *Server
	for i := range servers {
		if servers[i].Name == name {
			target = &servers[i]
			break
		}
	}
	if target == nil {
		return "", "", fmt.Errorf("server %q not found in %s", name, c.Path)
	}
	if target.Remote {
		return "", "", fmt.Errorf("server %q is remote and cannot be wrapped", name)
	}
	if target.Wrapped {
		return "", "", fmt.Errorf("server %q is already wrapped", name)
	}
	if len(target.Command) == 0 {
		return "", "", fmt.Errorf("server %q has no command", name)
	}

	old := append([]string(nil), target.Command...)
	next := append([]string{"run", "--"}, old...)

	// The new command is assembled from the original elements as written, so a
	// non-string argument, or a string escaped the user's way, comes through intact.
	elements := []string{`"run"`, `"--"`}
	for _, element := range target.raw {
		elements = append(elements, strings.TrimSpace(string(element)))
	}
	quoted, err := json.Marshal(command)
	if err != nil {
		return "", "", err
	}

	var bytes []byte
	if c.Style == StyleOpenCode {
		elements = append([]string{string(quoted)}, elements...)
		bytes, err = sjson.SetRawBytes(c.raw, c.path(name, "command"), []byte("["+strings.Join(elements, ",")+"]"))
	} else {
		bytes, err = sjson.SetRawBytes(c.raw, c.path(name, "command"), quoted)
		if err == nil {
			bytes, err = sjson.SetRawBytes(bytes, c.path(name, "args"), []byte("["+strings.Join(elements, ",")+"]"))
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("rewrite %s: %w", c.Path, err)
	}
	// Check the edit did what was asked before reporting success: a name the path
	// syntax misreads would otherwise add a stray entry or change nothing.
	if err := c.verifyWrap(bytes, servers, name, append([]string{command}, next...)); err != nil {
		return "", "", err
	}

	c.raw = bytes
	c.changed = true
	return strings.Join(old, " "), command + " " + strings.Join(next, " "), nil
}

// verifyWrap confirms that edited holds the same servers as before, with only the
// named one changed, and that one running want.
func (c *Config) verifyWrap(edited []byte, before []Server, name string, want []string) error {
	fail := func() error {
		return fmt.Errorf("server %q in %s could not be rewritten safely; edit it by hand", name, c.Path)
	}
	after, err := (&Config{Path: c.Path, Style: c.Style, raw: edited}).Servers()
	if err != nil || len(after) != len(before) {
		return fail()
	}
	for i := range after {
		expect := before[i].Command
		if before[i].Name == name {
			expect = want
		}
		if after[i].Name != before[i].Name || strings.Join(after[i].Command, "\x00") != strings.Join(expect, "\x00") {
			return fail()
		}
	}
	return nil
}

// Bytes returns the current (possibly edited) file contents.
func (c *Config) Bytes() []byte { return c.raw }

// Changed reports whether Wrap has modified the config.
func (c *Config) Changed() bool { return c.changed }

// Write backs up the original and writes the edited config.
func (c *Config) Write() (string, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(c.Path); err == nil {
		mode = info.Mode().Perm()
	}
	backup, err := Backup(c.Path, c.original, mode)
	if err != nil {
		return "", fmt.Errorf("backup %s: %w", c.Path, err)
	}
	if err := WriteFileAtomic(c.Path, c.raw, mode); err != nil {
		return "", fmt.Errorf("write %s: %w", c.Path, err)
	}
	c.original = append([]byte(nil), c.raw...)
	c.changed = false
	return backup, nil
}

// Backup saves a config's previous contents beside it and returns the backup's path.
//
// The name carries the time to the second. It never replaces an existing file: two
// edits within one second would otherwise overwrite the only copy of the file as the
// user wrote it. A numeric suffix is added until the name is free.
func Backup(path string, original []byte, mode os.FileMode) (string, error) {
	base := path + ".blackbox-backup-" + time.Now().Format("20060102-150405")
	for n := 0; ; n++ {
		name := base
		if n > 0 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(original)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
}

// WriteFileAtomic replaces a file's contents by writing a temporary file in the same
// directory and renaming it over the original, so a crash or a full disk part-way
// leaves the old config intact instead of a truncated one an agent cannot start with.
//
// A config that is a symbolic link (a dotfiles checkout, say) stays a link: the file
// it points to is the one replaced.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".blackbox-tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		// CreateTemp makes the file private; give it the mode the config had.
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

func (c *Config) path(name, field string) string {
	return c.Style.Key() + "." + escapeKey(name) + "." + field
}

// escapeKey makes a server name a literal key in an sjson path. sjson and the gjson
// lookups behind it give meaning to many characters (".", "*", "?", "|", "#", "@",
// "!", a leading ":", ...), and both take a backslash before any character to mean
// that character itself, so everything outside a plainly safe set is escaped rather
// than chasing the list.
func escapeKey(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		safe := ch >= 0x80 || ch == '_' ||
			(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !safe {
			b.WriteByte('\\')
		}
		b.WriteByte(ch)
	}
	return b.String()
}
