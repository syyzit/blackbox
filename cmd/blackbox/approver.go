package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/syyzit/blackbox/internal/policy"
)

// terminalApprover asks on the controlling terminal (/dev/tty, or the console on
// Windows), never on the MCP stream, so the agent cannot answer its own prompt.
//
// One reader goroutine owns the terminal for the life of the process. A blocking read
// cannot be cancelled, so a per-prompt reader that outlived its timeout would swallow
// the answer meant for the next prompt. With a single reader, answers that arrive
// after a prompt timed out are discarded before the next prompt is shown.
type terminalApprover struct {
	timeout time.Duration
	open    func() (io.Reader, io.Writer, error)

	turn chan struct{} // serializes prompts; a buffered channel so waiting can time out

	mu    sync.Mutex
	lines chan string // nil until the terminal is open
	out   io.Writer
}

func newTerminalApprover(timeout time.Duration, open func() (io.Reader, io.Writer, error)) *terminalApprover {
	if timeout <= 0 {
		timeout = time.Minute
	}
	return &terminalApprover{timeout: timeout, open: open, turn: make(chan struct{}, 1)}
}

// openConsole opens the terminal the user is sitting at, independent of the
// redirected stdin and stdout that carry the MCP stream.
func openConsole() (io.Reader, io.Writer, error) {
	if runtime.GOOS == "windows" {
		in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	return tty, tty, nil
}

// Approve shows the request and waits for y/yes. No terminal, a closed terminal, or
// no answer within the timeout is a denial.
func (t *terminalApprover) Approve(r policy.ApprovalRequest) bool {
	deadline := time.NewTimer(t.timeout)
	defer deadline.Stop()

	select {
	case t.turn <- struct{}{}:
		defer func() { <-t.turn }()
	case <-deadline.C:
		fmt.Fprintf(os.Stderr, "blackbox · %s timed out waiting for another approval: denied\n", r.Tool)
		return false
	}

	lines, out, err := t.terminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · %s needs approval but there is no terminal: denied\n", r.Tool)
		return false
	}

	// Anything typed before this prompt was shown answered an earlier one.
	for drained := false; !drained; {
		select {
		case _, ok := <-lines:
			if !ok {
				t.reset()
				return false
			}
		default:
			drained = true
		}
	}

	args := r.Args
	if len(args) > 240 {
		args = args[:240] + "…"
	}
	fmt.Fprintf(out, "\nblackbox · approval required\n  server  %s\n  tool    %s\n  args    %s\n  reason  %s\nallow? [y/N] ",
		printable(r.Server), printable(r.Tool), printable(args), printable(r.Reason))

	select {
	case line, ok := <-lines:
		if !ok {
			t.reset()
			return false
		}
		line = strings.ToLower(strings.TrimSpace(line))
		return line == "y" || line == "yes"
	case <-deadline.C:
		fmt.Fprintln(out, "\nblackbox · no answer: denied")
		return false
	}
}

func (t *terminalApprover) terminal() (<-chan string, io.Writer, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lines != nil {
		return t.lines, t.out, nil
	}
	in, out, err := t.open()
	if err != nil {
		return nil, nil, err
	}
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	t.lines, t.out = lines, out
	return lines, out, nil
}

// reset forgets a terminal whose reader ended, so the next prompt opens it again.
func (t *terminalApprover) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines, t.out = nil, nil
}

// printable neutralizes control characters, so arguments chosen by the agent cannot
// move the cursor or rewrite the prompt with terminal escape sequences.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, s)
}
