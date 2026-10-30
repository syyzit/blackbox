// Package event defines the normalized record of a single protocol frame.
package event

import "time"

// Direction is the direction a frame travelled relative to the proxy.
type Direction string

const (
	ClientToServer Direction = "c2s"
	ServerToClient Direction = "s2c"
)

// Event status values.
const (
	StatusPending       = "pending"
	StatusOK            = "ok"
	StatusError         = "error"
	StatusToolError     = "tool_error"
	StatusInputRequired = "input_required"
	StatusNotify        = "notify"
	StatusDenied        = "denied"
)

// ToolDef is a hashed tool definition observed in a tools/list response.
type ToolDef struct {
	Name    string `json:"name"`
	Hash    string `json:"hash"`
	Changed bool   `json:"changed,omitempty"`
}

// Event is one recorded protocol frame.
//
// Raw holds the exact bytes that crossed the wire (without the trailing newline),
// after secret redaction. Parsed fields are best-effort metadata for indexing.
type Event struct {
	SessionID string    `json:"sessionId"`
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	Direction Direction `json:"direction"`

	Method string `json:"method,omitempty"`
	Tool   string `json:"tool,omitempty"`
	ID     string `json:"id,omitempty"`

	// Raw is the exact frame as received (without the trailing newline), redacted.
	Raw string `json:"raw"`

	// Args is the request/notification params as compact JSON.
	Args string `json:"args,omitempty"`
	// Result is the response result as compact JSON.
	Result string `json:"result,omitempty"`
	// Error is the JSON-RPC error, if the frame is an error response.
	Error string `json:"error,omitempty"`
	// ResultType is complete / input_required / an extension value.
	ResultType string `json:"resultType,omitempty"`
	// Status classifies the frame: pending, ok, error, tool_error, input_required, notify.
	Status string `json:"status,omitempty"`
	// DurationMS is populated on responses and measures the matching request.
	DurationMS int64 `json:"durationMs,omitempty"`
	// Size is the byte size of the frame as received.
	Size int `json:"size,omitempty"`
	// ToolDefs is populated on tools/list responses.
	ToolDefs []ToolDef `json:"toolDefs,omitempty"`
	// Decision and DecisionReason are set when a policy gate evaluated the frame.
	Decision       string `json:"decision,omitempty"`
	DecisionReason string `json:"decisionReason,omitempty"`
	// Synthetic marks a frame Black Box produced itself, such as a block notice.
	Synthetic bool `json:"synthetic,omitempty"`
	// UsageIn/UsageOut are token counts reported by the server, when present.
	UsageIn  int `json:"usageIn,omitempty"`
	UsageOut int `json:"usageOut,omitempty"`
	// CostUSD is derived from usage and the configured price table.
	CostUSD float64 `json:"costUsd,omitempty"`
}

// Sink receives events as they are recorded.
//
// Emit must not block for longer than a single small write; the proxy calls it on the
// hot path between the agent and the server.
type Sink interface {
	Emit(Event) error
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc func(Event) error

func (f SinkFunc) Emit(e Event) error { return f(e) }
