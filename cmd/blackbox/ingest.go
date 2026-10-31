package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/redact"
	"github.com/spacecr8ed/blackbox/internal/store"
)

// ingestPayload is the envelope an editor plugin sends on stdin.
type ingestPayload struct {
	V          int             `json:"v"`
	Source     string          `json:"source"`
	SessionID  string          `json:"sessionId"`
	CallID     string          `json:"callId"`
	Tool       string          `json:"tool"`
	Agent      string          `json:"agent"`
	Input      json.RawMessage `json:"input"`
	Result     json.RawMessage `json:"result"`
	Error      string          `json:"error"`
	Status     string          `json:"status"` // completed or error
	DurationMS int64           `json:"durationMs"`
	At         time.Time       `json:"at"`
}

// cmdIngest appends one externally recorded tool call so built-in editor and shell
// tools show up in the same archive as MCP traffic.
func cmdIngest(args []string) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	var p ingestPayload
	if err := json.Unmarshal(data, &p); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox: ingest: invalid JSON:", err)
		return 1
	}
	if p.SessionID == "" || p.Tool == "" {
		fmt.Fprintln(os.Stderr, "blackbox: ingest: sessionId and tool are required")
		return 1
	}
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	duration := time.Duration(p.DurationMS) * time.Millisecond
	if duration < 0 || duration > 24*time.Hour {
		duration = 0
	}

	red := redact.Default()
	callID := p.CallID
	if callID == "" {
		callID = fmt.Sprintf("ext-%d", p.At.UnixNano())
	}

	input := p.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	argsJSON := red.String(compactJSON(input))

	reqFrame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      callID,
		"method":  "tools/call",
		"params":  map[string]any{"name": p.Tool, "arguments": input},
	})

	failed := p.Status == "error" || p.Error != ""
	status := event.StatusOK
	errText := ""
	respFrame := map[string]any{"jsonrpc": "2.0", "id": callID}
	resultJSON := ""
	if failed {
		status = event.StatusError
		errText = red.String(p.Error)
		if errText == "" {
			errText = "tool failed"
		}
		respFrame["error"] = map[string]any{"code": -32000, "message": errText}
	} else {
		result := p.Result
		if len(result) == 0 {
			result = json.RawMessage(`{}`)
		}
		resultJSON = red.String(compactJSON(result))
		respFrame["result"] = result
	}
	respBytes, _ := json.Marshal(respFrame)

	events := []event.Event{
		{
			At:        p.At.Add(-duration),
			Direction: event.ClientToServer,
			Method:    "tools/call",
			Tool:      p.Tool,
			ID:        callID,
			CallID:    callID,
			Status:    event.StatusPending,
			Args:      argsJSON,
			Size:      len(reqFrame),
			Raw:       red.String(string(reqFrame)),
		},
		{
			At:         p.At,
			Direction:  event.ServerToClient,
			Method:     "tools/call",
			Tool:       p.Tool,
			ID:         callID,
			CallID:     callID,
			Status:     status,
			Result:     resultJSON,
			Error:      errText,
			DurationMS: duration.Milliseconds(),
			Size:       len(respBytes),
			Raw:        red.String(string(respBytes)),
		},
	}

	if err := store.AppendExternal(*home, p.SessionID, []string{"opencode", "builtins"}, events); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox: ingest:", err)
		return 1
	}
	return 0
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
