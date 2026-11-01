// Command mcpshell is a deliberately small MCP server for dogfooding.
//
// It exposes one tool, run_command, which executes an allowlisted program directly
// (never through a shell) rooted at a directory. It exists so an agent working on Black
// Box can have its shell work recorded by Black Box itself.
//
// Security model:
//   - no shell: arguments are passed literally, so pipes and redirection do nothing
//   - an allowlist of programs, plus read-only forms of a few git subcommands, with
//     repository-configured diff drivers, textconv and fsmonitor switched off
//   - the working directory and any path argument must resolve inside the root
//   - find cannot run programs or delete files
//   - a timeout and an output cap
//
// It is not a sandbox: go and make build and run code from the repository by design.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/jsonrpc"
)

const version = "0.1.0"

func main() {
	root := flag.String("root", "", "directory commands are confined to (default: cwd)")
	allow := flag.String("allow", "go,git,make,ls,cat,head,tail,wc,grep,rg,find,pwd,echo,gofmt", "comma-separated allowlist")
	timeout := flag.Duration("timeout", 30*time.Second, "default command timeout")
	flag.Parse()

	if *root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "mcpshell:", err)
			os.Exit(1)
		}
		*root = wd
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpshell:", err)
		os.Exit(1)
	}

	s := &server{
		root:      abs,
		allow:     splitList(*allow),
		timeout:   *timeout,
		maxOutput: 64 << 10,
	}
	if err := s.serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcpshell:", err)
		os.Exit(1)
	}
}

type server struct {
	root      string
	allow     map[string]bool
	timeout   time.Duration
	maxOutput int
}

// gitSubcommands keeps repository inspection available without letting a tool call
// rewrite history or publish anything.
var gitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true,
	"branch": true, "rev-parse": true, "ls-files": true, "describe": true,
	"blame": true, "shortlog": true, "remote": true, "tag": true,
}

type callArgs struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Cwd       string   `json:"cwd"`
	TimeoutMS int      `json:"timeout_ms"`
}

func (s *server) serve(r io.Reader, w io.Writer) error {
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
			continue
		}
		if err := s.respond(w, &msg); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *server) respond(w io.Writer, msg *jsonrpc.Message) error {
	switch msg.Method {
	case "initialize":
		return s.writeResult(w, msg.ID, map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mcpshell", "version": version},
		})
	case "ping":
		return s.writeResult(w, msg.ID, map[string]any{})
	case "tools/list":
		return s.writeResult(w, msg.ID, map[string]any{"tools": []any{runCommandTool()}})
	case "tools/call":
		return s.callTool(w, msg)
	default:
		return s.writeError(w, msg.ID, -32601, "method not found: "+msg.Method)
	}
}

func runCommandTool() map[string]any {
	return map[string]any{
		"name":        "run_command",
		"description": "Run an allowlisted command inside the repository. No shell: pipes and redirection are not interpreted. Use args for arguments. Paths must stay inside the repository; git is limited to read-only use.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string", "description": "Program to run, e.g. go"},
				"args":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"cwd":        map[string]any{"type": "string", "description": "Directory relative to the repo root"},
				"timeout_ms": map[string]any{"type": "number"},
			},
			"required": []string{"command"},
		},
	}
}

func (s *server) callTool(w io.Writer, msg *jsonrpc.Message) error {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return s.writeError(w, msg.ID, -32602, "invalid params")
	}
	if p.Name != "run_command" {
		return s.writeResult(w, msg.ID, toolResult("unknown tool: "+p.Name, true))
	}
	var a callArgs
	if err := json.Unmarshal(p.Arguments, &a); err != nil {
		return s.writeResult(w, msg.ID, toolResult("invalid arguments: "+err.Error(), true))
	}

	text, failed := s.execute(a)
	return s.writeResult(w, msg.ID, toolResult(text, failed))
}

func toolResult(text string, isError bool) map[string]any {
	res := map[string]any{
		"resultType": "complete",
		"content":    []map[string]any{{"type": "text", "text": text}},
	}
	if isError {
		res["isError"] = true
	}
	return res
}

func (s *server) execute(a callArgs) (string, bool) {
	if strings.TrimSpace(a.Command) == "" {
		return "missing command", true
	}
	if !s.allow[a.Command] {
		return fmt.Sprintf("command %q is not allowlisted (allowed: %s)", a.Command, strings.Join(keys(s.allow), ", ")), true
	}
	dir, err := s.resolve(a.Cwd)
	if err != nil {
		return err.Error(), true
	}
	if err := checkArgs(s.root, dir, a.Command, a.Args); err != nil {
		return err.Error(), true
	}
	argv := a.Args
	if a.Command == "git" {
		argv = append(gitPrefix(a.Args[0]), a.Args[1:]...)
	}

	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = s.timeout
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.Command, argv...)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()

	text := string(out)
	if len(text) > s.maxOutput {
		text = text[:s.maxOutput] + "\n… output truncated"
	}

	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(runErr, &exitErr):
			code = exitErr.ExitCode()
		case ctx.Err() == context.DeadlineExceeded:
			code = -1
			text += "\ncommand timed out"
		default:
			code = -1
			text += "\n" + runErr.Error()
		}
	}

	header := fmt.Sprintf("$ %s %s\ncwd %s\nexit %d\n", a.Command, strings.Join(a.Args, " "), dir, code)
	return header + text, code != 0
}

func (s *server) resolve(cwd string) (string, error) {
	dir := s.root
	if cwd != "" {
		if looksAbsolute(cwd) {
			dir = cwd
		} else {
			dir = filepath.Join(s.root, cwd)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !within(s.root, abs) {
		return "", fmt.Errorf("cwd %q is outside the repo root", cwd)
	}
	return abs, nil
}

func (s *server) writeResult(w io.Writer, id json.RawMessage, result any) error {
	return writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *server) writeError(w io.Writer, id json.RawMessage, code int, message string) error {
	return writeJSON(w, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
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

func splitList(s string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out[part] = true
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
