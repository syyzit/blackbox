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
