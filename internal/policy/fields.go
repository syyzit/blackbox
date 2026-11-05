package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Field rules match decoded argument values instead of the raw JSON text.
//
// A key is a dotted path into the tool's arguments: "path", "options.mode", "args.0".
// A "*" segment stands for any array element or object key, and the rule matches when
// any value it reaches matches. The value is a glob (* and ?) compared with the
// argument as text: strings as they are, numbers and booleans as written, objects and
// arrays as compact JSON.
//
// "paths" works like "match" but treats values as file paths: backslashes become
// slashes and the path is cleaned first, so "/tmp/../etc/passwd" is checked as
// "/etc/passwd".

type fieldMatcher struct {
	key   string
	path  []string
	glob  *regexp.Regexp
	paths bool
}

func compileFields(match map[string]string, isPath bool) ([]fieldMatcher, error) {
	keys := make([]string, 0, len(match))
	for k := range match {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic order for errors and tests
	var out []fieldMatcher
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("empty field path")
		}
		segs := strings.Split(k, ".")
		for _, s := range segs {
			if s == "" {
				return nil, fmt.Errorf("field path %q has an empty segment", k)
			}
		}
		glob, err := regexp.Compile(wildcardPattern(match[k]))
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		out = append(out, fieldMatcher{key: k, path: segs, glob: glob, paths: isPath})
	}
	return out, nil
}

// matchFields reports whether every matcher finds a matching value in args.
func matchFields(ms []fieldMatcher, args any) bool {
	for _, m := range ms {
		if !m.matches(args) {
			return false
		}
	}
	return true
}

func (m fieldMatcher) matches(args any) bool {
	for _, v := range lookup(args, m.path) {
		s := fieldText(v)
		if m.paths {
			s = cleanPath(s)
		}
		if m.glob.MatchString(s) {
			return true
		}
	}
	return false
}

// lookup returns every value the path reaches.
func lookup(v any, segs []string) []any {
	if len(segs) == 0 {
		return []any{v}
	}
	seg, rest := segs[0], segs[1:]
	var out []any
	switch t := v.(type) {
	case map[string]any:
		if seg == "*" {
			for _, child := range t {
				out = append(out, lookup(child, rest)...)
			}
		} else if child, ok := t[seg]; ok {
			out = append(out, lookup(child, rest)...)
		}
	case []any:
		if seg == "*" {
			for _, child := range t {
				out = append(out, lookup(child, rest)...)
			}
		} else if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(t) {
			out = append(out, lookup(t[i], rest)...)
		}
	}
	return out
}

func fieldText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(t)
		return strings.TrimRight(buf.String(), "\n")
	}
}

func cleanPath(s string) string {
	if s == "" {
		return s
	}
	return path.Clean(strings.ReplaceAll(s, `\`, "/"))
}

// decodeArguments parses the tool arguments for field rules. Explicit arguments win;
// otherwise they are taken from MCP tools/call params ({"name":…,"arguments":{…}}).
func decodeArguments(r Request) any {
	raw := r.Arguments
	if len(raw) == 0 {
		var params struct {
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(r.Args), &params) == nil {
			raw = params.Arguments
		}
	}
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}
