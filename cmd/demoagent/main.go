// Command demoagent is a tiny MCP client used by the demo.
//
// With no arguments it talks to its own stdin/stdout, which is how Black Box drives
// it during replay. With arguments it spawns that command and records through it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spacecr8ed/blackbox/internal/mcptest"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func main() {
	var (
		cli *mcptest.Client
		err error
	)
	if len(os.Args) > 1 {
		cli, err = mcptest.Start(os.Args[1:]...)
	} else {
		cli = mcptest.NewClient(nopCloser{os.Stdout}, os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "demoagent:", err)
		os.Exit(1)
	}

	steps := []struct {
		method string
		params map[string]any
	}{
		{"tools/list", map[string]any{}},
		{"tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "hello from the agent"}}},
		{"tools/call", map[string]any{"name": "add", "arguments": map[string]any{"a": 2, "b": 40}}},
	}

	for _, step := range steps {
		res, err := cli.Call(step.method, step.params)
		if err != nil {
			fmt.Fprintln(os.Stderr, "demoagent:", err)
			os.Exit(1)
		}
		summary := summarize(res)
		fmt.Fprintf(os.Stderr, "agent · %-10s %s\n", step.method, summary)
	}

	_ = cli.Close()
}

func summarize(raw json.RawMessage) string {
	var v struct {
		Tools   []struct{ Name string } `json:"tools"`
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	if len(v.Tools) > 0 {
		names := make([]string, 0, len(v.Tools))
		for _, t := range v.Tools {
			names = append(names, t.Name)
		}
		return fmt.Sprintf("%d tools: %v", len(v.Tools), names)
	}
	if len(v.Content) > 0 {
		return v.Content[0].Text
	}
	return "ok"
}
