package store

import "strings"

// Source is an agent whose tool calls are recorded outside the MCP proxy: by an editor
// plugin or by the agent's own hooks.
type Source struct {
	// Name labels the session and is the server name policy rules match against.
	Name string
	// Prefix starts the Black Box session id for this agent's sessions.
	Prefix string
}

var (
	// OpenCode is recorded by plugins/opencode-blackbox.
	OpenCode = Source{Name: "opencode", Prefix: "oc-"}
	// ClaudeCode is recorded by `blackbox hook`, run from Claude Code's hooks.
	ClaudeCode = Source{Name: "claude-code", Prefix: "cc-"}
)

var sources = []Source{OpenCode, ClaudeCode}

// SourceByName finds a source by the name a plugin sends. An empty name is OpenCode,
// the only source that existed before the field did.
func SourceByName(name string) (Source, bool) {
	if name == "" {
		return OpenCode, true
	}
	for _, s := range sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// IsExternalSession reports whether a session id belongs to an external source rather
// than to an MCP connection.
func IsExternalSession(id string) bool {
	_, ok := SourceOfSession(id)
	return ok
}

// SourceOfSession returns the external source a session id belongs to.
func SourceOfSession(id string) (Source, bool) {
	for _, s := range sources {
		if strings.HasPrefix(id, s.Prefix) {
			return s, true
		}
	}
	return Source{}, false
}

func (s Source) argv() []string { return []string{s.Name, "builtins"} }
