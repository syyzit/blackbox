package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/policy"
)

func pipeTerminal() (*io.PipeWriter, func() (io.Reader, io.Writer, error)) {
	r, w := io.Pipe()
	return w, func() (io.Reader, io.Writer, error) { return r, io.Discard, nil }
}

func TestApproverLateAnswerDoesNotApproveNextPrompt(t *testing.T) {
	keys, open := pipeTerminal()
	a := newTerminalApprover(50*time.Millisecond, open)

	if a.Approve(policy.ApprovalRequest{Tool: "first"}) {
		t.Fatal("unanswered prompt was approved")
	}
	// The user answers the first prompt after it timed out.
	if _, err := io.WriteString(keys, "y\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // let the reader pick it up

	done := make(chan bool, 1)
	go func() { done <- newApproverCall(a, "second") }()
	time.Sleep(10 * time.Millisecond)
	if _, err := io.WriteString(keys, "n\n"); err != nil {
		t.Fatal(err)
	}
	if <-done {
		t.Fatal("the late answer to the first prompt approved the second")
	}
}

func TestApproverAnswersReachTheRightPrompt(t *testing.T) {
	keys, open := pipeTerminal()
	a := newTerminalApprover(time.Second, open)

	done := make(chan bool, 1)
	go func() { done <- newApproverCall(a, "one") }()
	time.Sleep(10 * time.Millisecond)
	io.WriteString(keys, "yes\n")
	if !<-done {
		t.Fatal("answered prompt was denied")
	}
}

func TestApproverDeniesWithoutTerminal(t *testing.T) {
	a := newTerminalApprover(time.Second, func() (io.Reader, io.Writer, error) {
		return nil, nil, errors.New("no tty")
	})
	if a.Approve(policy.ApprovalRequest{Tool: "x"}) {
		t.Fatal("approved without a terminal")
	}
}

func TestPrintableStripsEscapes(t *testing.T) {
	got := printable("ok\x1b[2K\rallow? y\n")
	if strings.ContainsAny(got, "\x1b\r\n") {
		t.Fatalf("control characters survived: %q", got)
	}
}

func newApproverCall(a *terminalApprover, tool string) bool {
	return a.Approve(policy.ApprovalRequest{Tool: tool})
}
