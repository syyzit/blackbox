// Package proxy runs an MCP server as a child process and forwards its stdio
// byte-for-byte while recording every frame.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
)

// Options configures a proxied run.
type Options struct {
	// Argv is the command that starts the MCP server.
	Argv []string
	// Env, when non-nil, replaces the child environment.
	Env []string
	// Stdin is the agent's side of the connection.
	Stdin io.Reader
	// Stdout is where server frames are written back to the agent.
	Stdout io.Writer
	// Stderr is passed through untouched.
	Stderr io.Writer
	// SessionID is stamped onto every recorded event.
	SessionID string
	// Sink receives recorded events. May be nil to disable recording.
	Sink event.Sink
}

// RunStdio proxies an MCP stdio server until the child exits.
//
// The proxy is transparent: bytes are forwarded exactly as received, and recording
// happens on a parsed copy. A recording failure never breaks the stream.
func RunStdio(ctx context.Context, o Options) error {
	if len(o.Argv) == 0 {
		return errors.New("proxy: empty command")
	}
	if o.Stdin == nil || o.Stdout == nil {
		return errors.New("proxy: stdin and stdout are required")
	}

	cmd := exec.CommandContext(ctx, o.Argv[0], o.Argv[1:]...)
	cmd.Env = o.Env
	cmd.Stderr = o.Stderr

	childIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdin pipe: %w", err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("proxy: start %q: %w", o.Argv[0], err)
	}

	p := &pipeline{sessionID: o.SessionID, sink: o.Sink}

	// Agent -> server. Closes the child's stdin on EOF so a piped run terminates.
	c2sErr := make(chan error, 1)
	go func() {
		err := p.pump(childIn, o.Stdin, event.ClientToServer)
		_ = childIn.Close()
		c2sErr <- err
	}()

	// Server -> agent.
	s2cErr := make(chan error, 1)
	go func() {
		s2cErr <- p.pump(o.Stdout, childOut, event.ServerToClient)
	}()

	// Wait for the server's stdout to close before cmd.Wait, per os/exec's contract.
	<-s2cErr
	waitErr := cmd.Wait()

	// The c2s pump can still be blocked reading an interactive terminal. Only collect
	// its result if it is already done; the process is exiting either way.
	select {
	case <-c2sErr:
	case <-time.After(100 * time.Millisecond):
	}

	if waitErr != nil {
		return waitErr
	}
	return nil
}

type pipeline struct {
	sessionID string
	sink      event.Sink
	seq       atomic.Int64
}

func (p *pipeline) pump(dst io.Writer, src io.Reader, dir event.Direction) error {
	br := bufio.NewReaderSize(src, 64*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := dst.Write(line); writeErr != nil {
				return writeErr
			}
			p.record(dir, line)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// record indexes a copy of the frame. Errors are deliberately swallowed: a broken
// recorder must never stall or kill the agent it is observing.
func (p *pipeline) record(dir event.Direction, line []byte) {
	if p.sink == nil {
		return
	}
	raw := trimEOL(line)
	e := event.Event{
		SessionID: p.sessionID,
		Seq:       p.seq.Add(1),
		At:        time.Now().UTC(),
		Direction: dir,
		Raw:       string(raw),
	}
	if msg, err := jsonrpc.Decode(raw); err == nil {
		e.Method = msg.Method
		e.Tool = msg.ToolName()
		e.ID = msg.IDString()
	}
	_ = p.sink.Emit(e)
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
