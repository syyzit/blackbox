package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func approvalSession(t *testing.T, home string) string {
	t.Helper()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApprovalLifecycle(t *testing.T) {
	home := t.TempDir()
	session := approvalSession(t, home)

	id, err := CreateApproval(home, Approval{
		SessionID: session, CallID: "call-1", Server: "srv", Tool: "echo",
		Args: `{"text":"hi"}`, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	pending, err := ListApprovals(home, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].Status != ApprovalPending {
		t.Fatalf("pending = %+v", pending)
	}

	if err := DecideApproval(home, id, true, "cli"); err != nil {
		t.Fatal(err)
	}
	got, err := ApprovalByID(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ApprovalApproved || got.DecidedBy != "cli" || got.DecidedAt.IsZero() {
		t.Fatalf("decided = %+v", got)
	}
	if err := DecideApproval(home, id, false, "ui"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decision = %v", err)
	}

	// A decision made before the wait starts is observed immediately.
	ok, err := WaitApproval(home, id, time.Second)
	if err != nil || !ok {
		t.Fatalf("wait = %v, %v", ok, err)
	}

	all, err := ListApprovals(home, false, 10)
	if err != nil || len(all) != 1 || all[0].Status != ApprovalApproved {
		t.Fatalf("all = %+v, %v", all, err)
	}
}

func TestWaitApprovalTimesOut(t *testing.T) {
	home := t.TempDir()
	session := approvalSession(t, home)
	id, err := CreateApproval(home, Approval{SessionID: session, Tool: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ok, err := WaitApproval(home, id, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a pending approval was treated as approved")
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("wait returned before the timeout")
	}
}

func TestWaitApprovalSeesDecision(t *testing.T) {
	home := t.TempDir()
	session := approvalSession(t, home)
	id, err := CreateApproval(home, Approval{SessionID: session, Tool: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = DecideApproval(home, id, true, "ui")
	}()
	ok, err := WaitApproval(home, id, 3*time.Second)
	if err != nil || !ok {
		t.Fatalf("wait = %v, %v", ok, err)
	}
}

func TestApprovalsCascadeOnPrune(t *testing.T) {
	home := t.TempDir()
	session := approvalSession(t, home)
	backdate(t, home, session, time.Now().Add(-48*time.Hour))
	if _, err := CreateApproval(home, Approval{SessionID: session, Tool: "echo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(home, PruneOptions{OlderThan: time.Hour}); err != nil {
		t.Fatal(err)
	}
	list, err := ListApprovals(home, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("prune left approvals behind: %+v", list)
	}
}

func TestApprovalPersistsAcrossOpens(t *testing.T) {
	home := t.TempDir()
	session := approvalSession(t, home)
	if _, err := CreateApproval(home, Approval{SessionID: session, Tool: "echo"}); err != nil {
		t.Fatal(err)
	}
	// Every operation opens its own connection, so a read here is a restart.
	list, err := ListApprovals(filepath.Clean(home), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("approval did not persist: %+v", list)
	}
}
