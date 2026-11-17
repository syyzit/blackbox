// Package jsonrpc holds a permissive JSON-RPC 2.0 envelope used by the proxy.
//
// The proxy must never reject a frame it does not understand: its job is to forward
// bytes. This package only extracts enough structure to index a frame.
package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Message is a JSON-RPC 2.0 request, response, or notification.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Decode parses a single frame, reading each field by its exact key (see exact.go).
// A leading byte order mark is skipped, since servers commonly accept one.
func Decode(line []byte) (*Message, error) {
	env, err := object(bytes.TrimPrefix(bytes.TrimSpace(line), bom))
	if err != nil {
		return nil, fmt.Errorf("decode json-rpc: %w", err)
	}
	m := Message{
		ID:     nonNull(env["id"]),
		Params: nonNull(env["params"]),
		Result: env["result"],
	}
	_ = json.Unmarshal(env["jsonrpc"], &m.JSONRPC)
	if raw, ok := env["method"]; ok {
		if err := json.Unmarshal(raw, &m.Method); err != nil {
			return nil, fmt.Errorf("decode json-rpc: method: %w", err)
		}
	}
	if raw := nonNull(env["error"]); len(raw) > 0 {
		var e Error
		if eo, err := object(raw); err == nil {
			_ = json.Unmarshal(eo["code"], &e.Code)
			_ = json.Unmarshal(eo["message"], &e.Message)
			e.Data = eo["data"]
		}
		m.Error = &e
	}
	return &m, nil
}

// DecodeBatch parses a JSON-RPC batch: a JSON array of messages. isBatch reports
// whether the frame is an array at all; elements that are not message objects are
// skipped, since a server must reject them anyway.
func DecodeBatch(line []byte) (msgs []*Message, isBatch bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, true
	}
	for _, r := range raw {
		if m, err := Decode(r); err == nil {
			msgs = append(msgs, m)
		}
	}
	return msgs, true
}

// IsRequest reports whether the frame is a request (has both method and id).
func (m *Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether the frame is a notification (method, no id).
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// IsResponse reports whether the frame is a response.
func (m *Message) IsResponse() bool {
	return m.Method == "" && (len(m.Result) > 0 || m.Error != nil)
}

// IDString renders the id as a string for indexing.
func (m *Message) IDString() string {
	if len(m.ID) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.ID, &s); err == nil {
		return s
	}
	return string(m.ID)
}

// ToolDefinition is a tool as advertised by a server.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ParseTools extracts tool definitions from a tools/list result. Fields are read by
// exact key, so a definition cannot show the client one description and the ledger
// another by spelling the key with different capitals.
func ParseTools(result json.RawMessage) ([]ToolDefinition, error) {
	res, err := object(result)
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if list := nonNull(res["tools"]); len(list) > 0 {
		if err := json.Unmarshal(list, &raw); err != nil {
			return nil, err
		}
	}
	tools := make([]ToolDefinition, 0, len(raw))
	for _, r := range raw {
		t, err := object(r)
		if err != nil {
			return nil, err
		}
		var d ToolDefinition
		if name := t["name"]; len(name) > 0 {
			if err := json.Unmarshal(name, &d.Name); err != nil {
				return nil, err
			}
		}
		_ = json.Unmarshal(t["description"], &d.Description)
		d.InputSchema = t["inputSchema"]
		tools = append(tools, d)
	}
	return tools, nil
}

// ParseResult reads resultType and the tool-level isError flag.
// An absent resultType is treated as "complete" per the spec's compatibility rule.
func ParseResult(result json.RawMessage) (resultType string, isError bool) {
	resultType = stringField(result, "resultType")
	if resultType == "" {
		resultType = "complete"
	}
	_ = json.Unmarshal(Field(result, "isError"), &isError)
	return resultType, isError
}

// Usage is token accounting reported by an MCP server, when it reports any.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ParseUsage extracts token usage from a tool result. MCP itself does not define
// billing, so this looks for the common shapes: a top-level "usage" object, or one
// under _meta.
func ParseUsage(result json.RawMessage) Usage {
	type tokens struct {
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	var r struct {
		Usage *tokens                    `json:"usage"`
		Meta  map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return Usage{}
	}
	pick := func(t *tokens) Usage {
		if t == nil {
			return Usage{}
		}
		in, out := t.InputTokens, t.OutputTokens
		if in == 0 {
			in = t.PromptTokens
		}
		if out == 0 {
			out = t.CompletionTokens
		}
		return Usage{InputTokens: in, OutputTokens: out}
	}
	if u := pick(r.Usage); u.InputTokens != 0 || u.OutputTokens != 0 {
		return u
	}
	if raw, ok := r.Meta["io.modelcontextprotocol/usage"]; ok {
		var t tokens
		if err := json.Unmarshal(raw, &t); err == nil {
			return pick(&t)
		}
	}
	return Usage{}
}

// RequestState extracts params.requestState (MRTR retries echo it).
func (m *Message) RequestState() string {
	if len(m.Params) == 0 {
		return ""
	}
	return stringField(m.Params, "requestState")
}

// HasInputResponses reports whether the frame carries MRTR inputResponses.
func (m *Message) HasInputResponses() bool {
	if len(m.Params) == 0 {
		return false
	}
	return len(nonNull(Field(m.Params, "inputResponses"))) > 0
}

// ResultRequestState reads result.requestState from an input_required result.
func ResultRequestState(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	return stringField(result, "requestState")
}

// SubscriptionID reads params._meta["io.modelcontextprotocol/subscriptionId"].
func (m *Message) SubscriptionID() string {
	if len(m.Params) == 0 {
		return ""
	}
	return stringField(Field(m.Params, "_meta"), "io.modelcontextprotocol/subscriptionId")
}

// ExternalID reads params._meta["ai.opencode/sessionID"], the OpenCode session id.
func (m *Message) ExternalID() string {
	if len(m.Params) == 0 {
		return ""
	}
	return stringField(Field(m.Params, "_meta"), "ai.opencode/sessionID")
}

// ToolName extracts params.name for tools/call frames.
func (m *Message) ToolName() string {
	if m.Method != "tools/call" || len(m.Params) == 0 {
		return ""
	}
	return stringField(m.Params, "name")
}
