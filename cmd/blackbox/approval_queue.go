package main

import (
	"fmt"
	"os"
	"time"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

// queueApprover handles approvals when there is no terminal: it persists a pending
// request, tells the user how to resolve it, and waits for the CLI or UI to decide.
type queueApprover struct {
	home      string
	sessionID string
	timeout   time.Duration
}

func (q queueApprover) Approve(r policy.ApprovalRequest) bool {
	approved, _ := q.ApproveSourced(r)
	return approved
}

func (q queueApprover) ApproveSourced(r policy.ApprovalRequest) (bool, string) {
	id, err := store.CreateApproval(q.home, store.Approval{
		SessionID: q.sessionID,
		CallID:    r.CallID,
		Server:    r.Server,
		Tool:      r.Tool,
		Args:      r.Args,
		Reason:    r.Reason,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · approval queue: %v\n", err)
		return false, ""
	}

	fmt.Fprintf(os.Stderr, "\nblackbox · approval %d pending for %s\n  blackbox approve %d   (or use the UI)\n  blackbox deny %d\n",
		id, r.Tool, id, id)

	ok, err := store.WaitApproval(q.home, id, q.timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · approval %d: %v\n", id, err)
		return false, ""
	}
	if !ok {
		// Distinguish a human denial from a timeout, and close a timed-out request so
		// it does not linger in the queue.
		if a, err := store.ApprovalByID(q.home, id); err == nil && a.Status == store.ApprovalDenied {
			return false, "queue"
		}
		_ = store.DecideApproval(q.home, id, false, "timeout")
		fmt.Fprintf(os.Stderr, "blackbox · approval %d timed out: denied\n", id)
		return false, ""
	}

	source := "queue"
	if a, err := store.ApprovalByID(q.home, id); err == nil && a.DecidedBy != "" {
		source = a.DecidedBy
	}
	return true, source
}
