package main

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

type stubFallback struct{ allow bool }

func (s stubFallback) Approve(policy.ApprovalRequest) bool { return s.allow }

func openSession(t *testing.T, home string) string {
	t.Helper()
	sess, err := store.Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitForPending(t *testing.T, home string) store.Approval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, err := store.ListApprovals(home, true, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			return list[0]
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
	return store.Approval{}
}

func TestQueueApproverWaitsForUI(t *testing.T) {
	home := t.TempDir()
	session := openSession(t, home)
	q := queueApprover{home: home, sessionID: session, timeout: 5 * time.Second}

	result := make(chan struct {
		ok  bool
		src string
	}, 1)
	go func() {
		ok, src := q.ApproveSourced(policy.ApprovalRequest{Tool: "echo", CallID: "call-7", Args: `{"text":"hi"}`, Reason: "test"})
		result <- struct {
			ok  bool
			src string
		}{ok, src}
	}()

	pending := waitForPending(t, home)
	if pending.Tool != "echo" || pending.CallID != "call-7" || pending.SessionID != session {
		t.Fatalf("pending = %+v", pending)
	}
	if err := store.DecideApproval(home, pending.ID, true, "ui"); err != nil {
		t.Fatal(err)
	}

	r := <-result
	if !r.ok || r.src != "ui" {
		t.Fatalf("result = %+v", r)
	}
}

func TestQueueApproverDeny(t *testing.T) {
	home := t.TempDir()
	session := openSession(t, home)
	q := queueApprover{home: home, sessionID: session, timeout: 5 * time.Second}

	result := make(chan bool, 1)
	go func() {
		ok, _ := q.ApproveSourced(policy.ApprovalRequest{Tool: "echo"})
		result <- ok
	}()
	pending := waitForPending(t, home)
	if err := store.DecideApproval(home, pending.ID, false, "cli"); err != nil {
		t.Fatal(err)
	}
	if <-result {
		t.Fatal("a denied request was approved")
	}
}

func TestQueueApproverTimesOutAndCloses(t *testing.T) {
	home := t.TempDir()
	session := openSession(t, home)
	q := queueApprover{home: home, sessionID: session, timeout: 300 * time.Millisecond}

	start := time.Now()
	if ok, _ := q.ApproveSourced(policy.ApprovalRequest{Tool: "echo"}); ok {
		t.Fatal("timeout approved the call")
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("returned before the timeout")
	}

	all, err := store.ListApprovals(home, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Status != store.ApprovalDenied || all[0].DecidedBy != "timeout" {
		t.Fatalf("timed-out approval = %+v", all)
	}
}

func TestTerminalFallsBackToQueue(t *testing.T) {
	term := newTerminalApprover(time.Second, func() (io.Reader, io.Writer, error) {
		return nil, nil, errors.New("no tty")
	})
	term.fallback = stubFallback{allow: true}

	ok, source := term.ApproveSourced(policy.ApprovalRequest{Tool: "echo"})
	if !ok || source != "queue" {
		t.Fatalf("fallback = %v, %q", ok, source)
	}
}
