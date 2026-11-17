package mcptest

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// A reply the handler is still computing when the client is closed is written before
// the stream closes. Otherwise a test that stops the client right after the handler
// was reached can lose the reply, depending on scheduling.
func TestCloseWaitsForAnswersInFlight(t *testing.T) {
	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	entered, release := make(chan struct{}), make(chan struct{})
	cli := NewClientWithHandler(fromClientW, toClientR, func(string, json.RawMessage) (any, error) {
		close(entered)
		<-release
		return map[string]any{"roots": []any{}}, nil
	})

	go func() { _, _ = io.WriteString(toClientW, `{"jsonrpc":"2.0","id":99,"method":"roots/list"}`+"\n") }()
	<-entered

	closed := make(chan struct{})
	go func() { _ = cli.Close(); close(closed) }()
	close(release)

	line, err := bufio.NewReader(fromClientR).ReadString('\n')
	if err != nil || !strings.Contains(line, `"id":99`) {
		t.Fatalf("reply lost on close: %q, %v", line, err)
	}
	<-closed
	_ = toClientW.Close()
}
