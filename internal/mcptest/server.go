// Package mcptest provides a fake MCP server and a minimal client used by the
// integration tests and the demo script.
package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
)

// Tool is an MCP tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Reply is what a fake tool returns.
type Reply struct {
	Text     string
	IsError  bool
	UsageIn  int
	UsageOut int
}

// Handler implements tool behavior.
type Handler func(name string, args json.RawMessage) Reply

// Server is a scriptable MCP stdio server.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
	Handle  Handler
}

// NewServer returns a server with a few deterministic tools.
func NewServer() *Server {
	return &Server{
		Name:    "fakeserver",
		Version: "0.1.0",
		Tools: []Tool{
			{
				Name:        "echo",
				Description: "Echo text back. Useful for testing the recorder.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			},
			{
				Name:        "add",
				Description: "Add two numbers.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
			},
			{
				Name:        "fail",
				Description: "Always fails. Useful for testing error capture.",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		Handle: defaultHandle,
	}
}

func defaultHandle(name string, args json.RawMessage) Reply {
	switch name {
	case "echo":
		var a struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &a)
		return Reply{Text: a.Text, UsageIn: 1200, UsageOut: 300}
	case "add":
		var a struct {
			A float64 `json:"a"`
			B float64 `json:"b"`
		}
		_ = json.Unmarshal(args, &a)
		return Reply{Text: fmt.Sprintf("%g", a.A+a.B)}
	case "fail":
		return Reply{Text: "intentional failure", IsError: true}
	default:
		return Reply{Text: "unknown tool: " + name, IsError: true}
	}
}

// Serve reads newline-delimited JSON-RPC from r and writes responses to w until EOF.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg jsonrpc.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			if err := s.writeError(w, nil, -32700, "parse error"); err != nil {
				return err
			}
			continue
		}
		if !msg.IsRequest() {
			continue // notifications and stray responses are ignored
		}
		if err := s.respond(w, &msg); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) respond(w io.Writer, msg *jsonrpc.Message) error {
	var (
		result any
		rpcErr *jsonrpc.Error
	)

	switch msg.Method {
	case "server/discover":
		result = map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": s.Name, "version": s.Version},
		}
	case "tools/list":
		result = map[string]any{"tools": s.Tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return s.writeError(w, msg.ID, -32602, "invalid params")
		}
		reply := s.Handle(p.Name, p.Arguments)
		payload := map[string]any{
			"resultType": "complete",
			"content":    []map[string]any{{"type": "text", "text": reply.Text}},
		}
		if reply.IsError {
			payload["isError"] = true
		}
		if reply.UsageIn > 0 || reply.UsageOut > 0 {
			payload["usage"] = map[string]any{
				"input_tokens":  reply.UsageIn,
				"output_tokens": reply.UsageOut,
			}
		}
		result = payload
	case "ping":
		result = map[string]any{}
	default:
		rpcErr = &jsonrpc.Error{Code: -32601, Message: "method not found: " + msg.Method}
	}

	if rpcErr != nil {
		return s.writeError(w, msg.ID, rpcErr.Code, rpcErr.Message)
	}
	return writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"result":  result,
	})
}

func (s *Server) writeError(w io.Writer, id json.RawMessage, code int, message string) error {
	return writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
