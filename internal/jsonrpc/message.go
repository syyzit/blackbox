// Package jsonrpc holds a permissive JSON-RPC 2.0 envelope used by the proxy.
//
// The proxy must never reject a frame it does not understand: its job is to forward
// bytes. This package only extracts enough structure to index a frame.
package jsonrpc

import (
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

// Decode parses a single frame.
func Decode(line []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("decode json-rpc: %w", err)
	}
	return &m, nil
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

// ParseTools extracts tool definitions from a tools/list result.
func ParseTools(result json.RawMessage) ([]ToolDefinition, error) {
	var r struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, err
	}
	return r.Tools, nil
}

// ParseResult reads resultType and the tool-level isError flag.
// An absent resultType is treated as "complete" per the spec's compatibility rule.
func ParseResult(result json.RawMessage) (resultType string, isError bool) {
	var r struct {
		ResultType string `json:"resultType"`
		IsError    bool   `json:"isError"`
	}
	_ = json.Unmarshal(result, &r)
	if r.ResultType == "" {
		r.ResultType = "complete"
	}
	return r.ResultType, r.IsError
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
	var p struct {
		RequestState string `json:"requestState"`
	}
	_ = json.Unmarshal(m.Params, &p)
	return p.RequestState
}

// HasInputResponses reports whether the frame carries MRTR inputResponses.
func (m *Message) HasInputResponses() bool {
	if len(m.Params) == 0 {
		return false
	}
	var p struct {
		InputResponses json.RawMessage `json:"inputResponses"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return false
	}
	return len(p.InputResponses) > 0 && string(p.InputResponses) != "null"
}

// ResultRequestState reads result.requestState from an input_required result.
func ResultRequestState(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var r struct {
		RequestState string `json:"requestState"`
	}
	_ = json.Unmarshal(result, &r)
	return r.RequestState
}

// SubscriptionID reads params._meta["io.modelcontextprotocol/subscriptionId"].
func (m *Message) SubscriptionID() string {
	if len(m.Params) == 0 {
		return ""
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Meta == nil {
		return ""
	}
	raw, ok := p.Meta["io.modelcontextprotocol/subscriptionId"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// ExternalID reads params._meta["ai.opencode/sessionID"], the OpenCode session id.
func (m *Message) ExternalID() string {
	if len(m.Params) == 0 {
		return ""
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Meta == nil {
		return ""
	}
	raw, ok := p.Meta["ai.opencode/sessionID"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// ToolName extracts params.name for tools/call frames.
func (m *Message) ToolName() string {
	if m.Method != "tools/call" || len(m.Params) == 0 {
		return ""
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return ""
	}
	return p.Name
}
