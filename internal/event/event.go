// Package event defines the normalized record of a single protocol frame.
package event

import "time"

// Direction is the direction a frame travelled relative to the proxy.
type Direction string

const (
	ClientToServer Direction = "c2s"
	ServerToClient Direction = "s2c"
)

// Event is one recorded protocol frame.
//
// Raw holds the exact bytes that crossed the wire (without the trailing newline).
// Parsed fields are best-effort metadata for indexing; Raw is the evidence.
type Event struct {
	SessionID string    `json:"sessionId"`
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	Direction Direction `json:"direction"`
	Method    string    `json:"method,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	ID        string    `json:"id,omitempty"`
	Raw       string    `json:"raw"`
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
