// Command demoagent is a tiny scriptable MCP client used by the demos.
//
// It talks to its own stdin/stdout by default, which is how Black Box drives it during
// replay, or spawns a command given after "--".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spacecr8ed/blackbox/internal/mcptest"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

type step struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func defaultSteps() []step {
	return []step{
		{Method: "tools/list", Params: json.RawMessage(`{}`)},
		{Method: "tools/call", Params: json.RawMessage(`{"name":"echo","arguments":{"text":"hello from the agent"}}`)},
		{Method: "tools/call", Params: json.RawMessage(`{"name":"add","arguments":{"a":2,"b":40}}`)},
	}
}

func main() {
	stepsPath := flag.String("steps", "", "JSON file with a list of {method, params}")
	flag.Parse()

	steps := defaultSteps()
	if *stepsPath != "" {
		b, err := os.ReadFile(*stepsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "demoagent:", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(b, &steps); err != nil {
			fmt.Fprintln(os.Stderr, "demoagent:", err)
			os.Exit(1)
		}
	}

	argv := flag.Args()
	for len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}

	var (
		cli *mcptest.Client
		err error
	)
	if len(argv) > 0 {
		cli, err = mcptest.Start(argv...)
	} else {
		cli = mcptest.NewClient(nopCloser{os.Stdout}, os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "demoagent:", err)
		os.Exit(1)
	}

	for _, s := range steps {
		if strings.HasPrefix(s.Method, "notifications/") {
			if err := cli.Notify(s.Method, json.RawMessage(s.Params)); err != nil {
				fmt.Fprintln(os.Stderr, "demoagent:", err)
				os.Exit(1)
			}
			continue
		}

		res, err := cli.Call(s.Method, json.RawMessage(s.Params))
		if err != nil {
			fmt.Fprintln(os.Stderr, "demoagent:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "agent · %-24s %s\n", s.Method, summarize(res))
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
		if len(names) > 6 {
			names = append(names[:6], "…")
		}
		return fmt.Sprintf("%d tools: %v", len(v.Tools), names)
	}
	if len(v.Content) > 0 {
		return v.Content[0].Text
	}
	return "ok"
}
