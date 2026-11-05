package redact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
)

// File is the on-disk format for extra redaction rules, read from
// <home>/redact.json:
//
//	{"rules": [
//	  {"name": "internal-token", "pattern": "itk_[A-Za-z0-9]{32}"},
//	  {"name": "customer-id", "pattern": "(cust_)[0-9]{8}", "replace": "${1}[REDACTED]"}
//	]}
//
// Patterns use Go regexp syntax. Replace defaults to "[REDACTED:<name>]" and may use
// $1-style references to keep part of the match.
type File struct {
	Rules []FileRule `json:"rules"`
}

// FileRule is one user-defined rule.
type FileRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Replace string `json:"replace,omitempty"`
}

// Load returns the built-in rules plus any user rules in path. User rules run first,
// so an organization's own token formats get their own labels. A missing file is not
// an error; an unreadable or invalid one is, so a typo cannot silently disable
// redaction the user asked for.
func Load(path string) (*Redactor, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("redact: read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("redact: parse %s: %w", path, err)
	}
	rules := make([]Rule, 0, len(f.Rules)+len(defaultRules))
	for i, fr := range f.Rules {
		if fr.Pattern == "" {
			return nil, fmt.Errorf("redact: %s: rule %d has no pattern", path, i)
		}
		re, err := regexp.Compile(fr.Pattern)
		if err != nil {
			return nil, fmt.Errorf("redact: %s: rule %d: %w", path, i, err)
		}
		if re.MatchString("") {
			return nil, fmt.Errorf("redact: %s: rule %d matches the empty string", path, i)
		}
		name := fr.Name
		if name == "" {
			name = fmt.Sprintf("custom-%d", i)
		}
		replace := fr.Replace
		if replace == "" {
			replace = "[REDACTED:" + name + "]"
		}
		rules = append(rules, Rule{Name: name, Re: re, Replace: replace})
	}
	rules = append(rules, defaultRules...)
	return New(rules...), nil
}
