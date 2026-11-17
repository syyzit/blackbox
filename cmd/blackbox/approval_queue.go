package main

import (
	"errors"
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
		return q.settleUnapproved(id)
	}

	source := "queue"
	if a, err := store.ApprovalByID(q.home, id); err == nil && a.DecidedBy != "" {
		source = a.DecidedBy
	}
	return true, source
}

// settleUnapproved resolves a request the wait did not see approved: denied by a
// human, or still pending when the wait gave up.
//
// It closes the request as timed out first and looks at the row only if that fails.
// The transition is a single conditional update, so exactly one of "timeout" and a
// human's answer wins. Reading the status first and closing afterwards would leave a
// gap in which an approval lands, the row says "approved", and the call is denied.
func (q queueApprover) settleUnapproved(id int64) (bool, string) {
	err := store.DecideApproval(q.home, id, false, "timeout")
	switch {
	case err == nil:
		fmt.Fprintf(os.Stderr, "blackbox · approval %d timed out: denied\n", id)
		return false, ""
	case !errors.Is(err, store.ErrAlreadyDecided):
		fmt.Fprintf(os.Stderr, "blackbox · approval %d: %v\n", id, err)
		return false, ""
	}

	// Someone decided before the timeout could: their answer stands.
	a, err := store.ApprovalByID(q.home, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · approval %d: %v\n", id, err)
		return false, ""
	}
	if a.Status != store.ApprovalApproved {
		return false, "queue"
	}
	if a.DecidedBy != "" {
		return true, a.DecidedBy
	}
	return true, "queue"
}
