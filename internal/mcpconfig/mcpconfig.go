// Package mcpconfig discovers and rewrites MCP server entries in agent config files.
//
// Edits are applied with sjson so the rest of the user's file — key order, formatting,
// unrelated settings — is preserved byte-for-byte.
package mcpconfig

import (
	"encoding/json"
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
		var entry struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			URL     string   `json:"url"`
			Type    string   `json:"type"`
		}
		_ = json.Unmarshal(entries[name], &entry)

		var cmd []string
		if c.Style == StyleOpenCode {
			var arrayEntry struct {
				Command []string `json:"command"`
				URL     string   `json:"url"`
				Type    string   `json:"type"`
			}
			_ = json.Unmarshal(entries[name], &arrayEntry)
			cmd = arrayEntry.Command
			entry.URL = arrayEntry.URL
			entry.Type = arrayEntry.Type
		} else if entry.Command != "" {
			cmd = append([]string{entry.Command}, entry.Args...)
		}

		remote := entry.URL != "" || entry.Type == "remote" || entry.Type == "sse" || entry.Type == "http"
		out = append(out, Server{
			Name:    name,
			Command: cmd,
			Remote:  remote,
			Wrapped: isWrapped(cmd),
			Type:    entry.Type,
		})
	}
	return out, nil
}

func isWrapped(cmd []string) bool {
	if len(cmd) < 3 {
		return false
	}
	return filepath.Base(cmd[0]) == "blackbox" && cmd[1] == "run" && cmd[2] == "--"
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

	var bytes []byte
	if c.Style == StyleOpenCode {
		bytes, err = sjson.SetBytes(c.raw, c.path(name, "command"), append([]string{command}, next...))
	} else {
		bytes, err = sjson.SetBytes(c.raw, c.path(name, "command"), command)
		if err == nil {
			bytes, err = sjson.SetBytes(bytes, c.path(name, "args"), next)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("rewrite %s: %w", c.Path, err)
	}

	c.raw = bytes
	c.changed = true
	return strings.Join(old, " "), command + " " + strings.Join(next, " "), nil
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
	backup := c.Path + ".blackbox-backup-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, c.original, mode); err != nil {
		return "", fmt.Errorf("backup %s: %w", c.Path, err)
	}
	if err := os.WriteFile(c.Path, c.raw, mode); err != nil {
		return "", fmt.Errorf("write %s: %w", c.Path, err)
	}
	c.original = append([]byte(nil), c.raw...)
	c.changed = false
	return backup, nil
}

func (c *Config) path(name, field string) string {
	return c.Style.Key() + "." + escapeKey(name) + "." + field
}

func escapeKey(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `?`, `\?`)
	return r.Replace(s)
}
