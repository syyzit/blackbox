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

	// apply, when set, does the rule's work instead of Re and Replace. It is for the
	// built-in rules a regexp alone cannot express; Re still describes what they
	// look for.
	apply func(string) string
}

func (rule Rule) redact(s string) string {
	if rule.apply != nil {
		return rule.apply(s)
	}
	return rule.Re.ReplaceAllString(s, rule.Replace)
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
		s = rule.redact(s)
	}
	return s
}

// HasSecrets is a cheap check used by tests and the UI to flag frames.
func (r *Redactor) HasSecrets(s string) bool {
	if r == nil {
		return false
	}
	for _, rule := range r.rules {
		if rule.apply != nil {
			if rule.apply(s) != s {
				return true
			}
		} else if rule.Re.MatchString(s) {
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
		// The BEGIN line and the key after it; see privatekey.go.
		Name:    "private-key",
		Re:      privateKeyBeginRe,
		Replace: privateKeyMark,
		apply:   redactPrivateKeys,
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
		Name:    "github-fine-grained-token",
		Re:      regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
		Replace: "[REDACTED:github-token]",
	},
	{
		Name:    "gitlab-token",
		Re:      regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
		Replace: "[REDACTED:gitlab-token]",
	},
	{
		Name:    "aws-access-key",
		Re:      regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		Replace: "[REDACTED:aws-access-key]",
	},
	{
		Name:    "google-api-key",
		Re:      regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		Replace: "[REDACTED:google-api-key]",
	},
	{
		Name:    "stripe-key",
		Re:      regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`),
		Replace: "[REDACTED:stripe-key]",
	},
	{
		Name:    "slack-token",
		Re:      regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{10,}`),
		Replace: "[REDACTED:slack-token]",
	},
	{
		Name:    "slack-webhook",
		Re:      regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/_-]{20,}`),
		Replace: "[REDACTED:slack-webhook]",
	},
	{
		Name:    "npm-token",
		Re:      regexp.MustCompile(`npm_[A-Za-z0-9]{36}`),
		Replace: "[REDACTED:npm-token]",
	},
	{
		Name:    "huggingface-token",
		Re:      regexp.MustCompile(`hf_[A-Za-z0-9]{30,}`),
		Replace: "[REDACTED:huggingface-token]",
	},
	{
		// Keeps the scheme, host and user, drops the password:
		// postgres://app:[REDACTED:password]@db:5432/prod
		// The user may be empty (redis://:password@host). A backslash is taken with
		// the character it escapes, so a JSON escape is never cut in half; a quote,
		// escaped or not, is never part of the password. Nor is a square bracket,
		// which a URL has to percent-encode there: that keeps the rule from reading
		// a [REDACTED:…] marker left by another rule as a password on a later pass.
		Name:    "url-credentials",
		Re:      regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@"\\]*:)(?:[^\s@/"\\\[\]]|\\[^\s/"])+@`),
		Replace: "${1}[REDACTED:password]@",
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
		// Keeps the key name, drops the value: "password": "hunter2". Text that was
		// already redacted ("password": "[REDACTED:secret]") is left alone on a
		// second pass. See assign.go for how the value is read.
		Name:    "secret-assignment",
		Re:      secretAssignmentRe,
		Replace: secretMark,
		apply:   redactAssignments,
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
