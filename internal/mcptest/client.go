package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
)

// Client is a tiny MCP client for driving a proxied server in tests.
type Client struct {
	w       io.WriteCloser
	cmd     *exec.Cmd
	lines   chan []byte
	readErr chan error

	mu      sync.Mutex
	nextID  int64
	timeout time.Duration
}

// NewClient talks over an existing stream pair.
func NewClient(w io.WriteCloser, r io.Reader) *Client {
	c := &Client{
		w:       w,
		lines:   make(chan []byte, 32),
		readErr: make(chan error, 1),
		timeout: 10 * time.Second,
	}
	go c.readLoop(r)
	return c
}

// Start launches argv and talks to its stdio.
func Start(argv ...string) (*Client, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := NewClient(in, out)
	c.cmd = cmd
	return c, nil
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		c.lines <- line
	}
	if err := sc.Err(); err != nil {
		c.readErr <- err
	}
}

// Send writes one raw frame, adding the trailing newline.
func (c *Client) Send(line []byte) error {
	_, err := c.w.Write(append(line, '\n'))
	return err
}

// Next returns the next frame from the server.
func (c *Client) Next() ([]byte, error) {
	select {
	case line := <-c.lines:
		return line, nil
	case err := <-c.readErr:
		return nil, err
	case <-time.After(c.timeout):
		return nil, fmt.Errorf("mcptest: timed out waiting for a frame")
	}
}

// Call sends a request and waits for its matching response.
func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.Send(b); err != nil {
		return nil, err
	}

	want := strconv.FormatInt(id, 10)
	for {
		line, err := c.Next()
		if err != nil {
			return nil, err
		}
		var msg jsonrpc.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.IDString() != want {
			continue
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

// Notify sends a notification (no id, no response).
func (c *Client) Notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.Send(b)
}

// Close closes the stream and, for a started process, waits for it to exit.
func (c *Client) Close() error {
	_ = c.w.Close()
	if c.cmd == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		return <-done
	}
}
