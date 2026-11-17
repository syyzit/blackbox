package main

import (
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/store"
)

// The wait gives up after its last poll; a decision can still land before the
// request is closed. Whatever the row ends up saying is what the call must do: a row
// that reads "approved" beside a denied call is a false audit record, and a user who
// was told "approved" watched the call fail.
func TestQueueApproverHonoursDecisionThatBeatsTheTimeout(t *testing.T) {
	cases := []struct {
		name       string
		decide     func(home string, id int64) error
		wantOK     bool
		wantSource string
		wantStatus string
		wantBy     string
	}{
		{"approved after the last poll",
			func(home string, id int64) error { return store.DecideApproval(home, id, true, "ui") },
			true, "ui", store.ApprovalApproved, "ui"},
		{"denied after the last poll",
			func(home string, id int64) error { return store.DecideApproval(home, id, false, "cli") },
			false, "queue", store.ApprovalDenied, "cli"},
		{"nobody decided",
			func(string, int64) error { return nil },
			false, "", store.ApprovalDenied, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			session := openSession(t, home)
			id, err := store.CreateApproval(home, store.Approval{SessionID: session, Tool: "echo"})
			if err != nil {
				t.Fatal(err)
			}
			// The state settleUnapproved is entered in: the wait has reported "not
			// approved" from a poll that saw the request pending.
			if err := tc.decide(home, id); err != nil {
				t.Fatal(err)
			}

			q := queueApprover{home: home, sessionID: session, timeout: time.Second}
			ok, source := q.settleUnapproved(id)
			if ok != tc.wantOK || source != tc.wantSource {
				t.Fatalf("settled as %v by %q, want %v by %q", ok, source, tc.wantOK, tc.wantSource)
			}
			got, err := store.ApprovalByID(home, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus || got.DecidedBy != tc.wantBy {
				t.Fatalf("row is %s by %q, want %s by %q", got.Status, got.DecidedBy, tc.wantStatus, tc.wantBy)
			}
		})
	}
}
