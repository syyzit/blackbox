package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// This file exists because encoding/json matches struct fields case-insensitively:
// decoding {"method":"ping","Method":"tools/call"} into a struct yields "tools/call",
// while a server that reads keys exactly sees "ping". A recorder that indexes one
// reading while the peer acts on another can be told anything. Every field the proxy
// relies on is therefore read by its exact key.

var bom = []byte{0xEF, 0xBB, 0xBF}

// object decodes a JSON object into its exact keys. A repeated key keeps its last
// value, which is what JSON.parse, Python's json and Go's map decoding all do.
func object(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return m, nil
}

// Field returns the value of an exact key in a JSON object, or nil when raw is not an
// object or has no such key.
func Field(raw []byte, key string) json.RawMessage {
	m, err := object(raw)
	if err != nil {
		return nil
	}
	return m[key]
}

// stringField returns an exact key's value when it is a JSON string.
func stringField(raw []byte, key string) string {
	var s string
	if v := Field(raw, key); len(v) > 0 && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func nonNull(raw json.RawMessage) json.RawMessage {
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	return raw
}

// Ambiguity explains why two JSON parsers could read a client frame differently, or
// returns "" when they could not. The gate refuses such frames: it cannot know which
// reading the server will act on, so it cannot say what it would be allowing.
//
// A frame is ambiguous when any object in it repeats a key (parsers disagree on first
// or last), when the envelope or a tools/call's params spell a field the protocol
// defines with different capitals (a case-insensitive parser reads it, an exact one
// does not), or when a tools/call names its tool with something that is not a string.
func Ambiguity(line []byte) string {
	line = bytes.TrimPrefix(bytes.TrimSpace(line), bom)
	if key, ok := duplicateKey(line); ok {
		return fmt.Sprintf("frame repeats the key %q", key)
	}
	env, err := object(line)
	if err != nil {
		return "frame is not a JSON-RPC message"
	}
	if key := caseVariant(env, "jsonrpc", "id", "method", "params", "result", "error"); key != "" {
		return fmt.Sprintf("frame spells a JSON-RPC field as %q", key)
	}
	raw, ok := env["method"]
	if !ok {
		return ""
	}
	var method string
	if json.Unmarshal(raw, &method) != nil {
		return "method is not a string"
	}
	if method != "tools/call" {
		return ""
	}

	params, err := object(env["params"])
	if err != nil {
		return "tools/call params are not an object"
	}
	if key := caseVariant(params, "name", "arguments", "_meta"); key != "" {
		return fmt.Sprintf("tools/call spells a field as %q", key)
	}
	var name string
	if json.Unmarshal(params["name"], &name) != nil {
		return "tools/call name is not a string"
	}
	return ""
}

// caseVariant returns a key that matches one of the known names except for its case.
func caseVariant(obj map[string]json.RawMessage, known ...string) string {
	for key := range obj {
		for _, k := range known {
			if key != k && strings.EqualFold(key, k) {
				return key
			}
		}
	}
	return ""
}

// duplicateKey finds a key that appears twice in the same object, at any depth. It
// reports false when there is none or the text is not JSON.
func duplicateKey(raw []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	// One set of seen keys per open object; a nil entry is an open array.
	var stack []map[string]bool
	inObject := func() bool { return len(stack) > 0 && stack[len(stack)-1] != nil }
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false // end of input, or not JSON
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, map[string]bool{})
				expectKey = true
			case '[':
				stack = append(stack, nil)
				expectKey = false
			default: // } or ]
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				expectKey = inObject()
			}
		case string:
			if expectKey {
				seen := stack[len(stack)-1]
				if seen[t] {
					return t, true
				}
				seen[t] = true
				expectKey = false
				continue
			}
			expectKey = inObject()
		default:
			expectKey = inObject()
		}
	}
}
