// Package redact removes secrets from recorded payloads before they are persisted.
//
// Rules are deliberately conservative: they target credential shapes, not entropy
// heuristics, so they do not mangle ordinary tool output.
package redact

import (
	"regexp"
	"strings"
)

// Rule is a named pattern and its replacement template.
//
// Replace uses Go's regexp expansion syntax, so "$1" keeps a captured prefix.
type Rule struct {
	Name    string
	Re      *regexp.Regexp
	Replace string
}

// Redactor applies a sequence of rules.
type Redactor struct {
	rules []Rule
}

// New builds a redactor from rules. Order matters.
func New(rules ...Rule) *Redactor { return &Redactor{rules: rules} }

// Rules exposes the configured rules for display in the UI.
func (r *Redactor) Rules() []Rule {
	if r == nil {
		return nil
	}
	return append([]Rule(nil), r.rules...)
}

// String redacts one payload.
func (r *Redactor) String(s string) string {
	if r == nil || s == "" {
		return s
	}
	for _, rule := range r.rules {
		s = rule.Re.ReplaceAllString(s, rule.Replace)
	}
	return s
}

// HasSecrets is a cheap check used by tests and the UI to flag frames.
func (r *Redactor) HasSecrets(s string) bool {
	if r == nil {
		return false
	}
	for _, rule := range r.rules {
		if rule.Re.MatchString(s) {
			return true
		}
	}
	return false
}

// Default returns the built-in credential rules.
func Default() *Redactor {
	return New(defaultRules...)
}

var defaultRules = []Rule{
	{
		Name:    "private-key",
		Re:      regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
		Replace: "[REDACTED:private-key]",
	},
	{
		// Before openai-key, whose sk- prefix also matches sk-ant- keys.
		Name:    "anthropic-key",
		Re:      regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}`),
		Replace: "[REDACTED:anthropic-key]",
	},
	{
		Name:    "openai-key",
		Re:      regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),
		Replace: "[REDACTED:openai-key]",
	},
	{
		Name:    "github-token",
		Re:      regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		Replace: "[REDACTED:github-token]",
	},
	{
		Name:    "aws-access-key",
		Re:      regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		Replace: "[REDACTED:aws-access-key]",
	},
	{
		Name:    "bearer-token",
		Re:      regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{16,}`),
		Replace: "Bearer [REDACTED:bearer-token]",
	},
	{
		Name:    "jwt",
		Re:      regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
		Replace: "[REDACTED:jwt]",
	},
	{
		// Keeps the key name, drops the value: "password": "hunter2"
		Name:    "secret-assignment",
		Re:      regexp.MustCompile(`(?i)("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|secret|password|passwd|authorization)"?\s*[:=]\s*"?)([^\s",}\]]{6,})`),
		Replace: "${1}[REDACTED:secret]",
	},
}

// PatternNames returns the default rule names, useful in help text.
func PatternNames() string {
	names := make([]string, 0, len(defaultRules))
	for _, r := range defaultRules {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}
