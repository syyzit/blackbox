package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Approval is one require_approval request waiting for a human.
type Approval struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"sessionId"`
	CallID    string    `json:"callId,omitempty"`
	Server    string    `json:"server"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	DecidedAt time.Time `json:"decidedAt,omitempty"`
	DecidedBy string    `json:"decidedBy,omitempty"`
}

// Approval statuses.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
)

// ErrAlreadyDecided is returned when a pending request was resolved by someone else.
var ErrAlreadyDecided = errors.New("store: approval is no longer pending")

// CreateApproval queues a require_approval request.
func CreateApproval(home string, a Approval) (int64, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	result, err := db.Exec(
		`INSERT INTO approvals (session_id, call_id, server, tool, args, reason, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.SessionID, a.CallID, a.Server, a.Tool, a.Args, a.Reason, ApprovalPending, a.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ApprovalByID reads one request.
func ApprovalByID(home string, id int64) (*Approval, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	return scanApproval(db.QueryRow(
		`SELECT id, session_id, call_id, server, tool, args, reason, status, created_at, decided_at, decided_by
		   FROM approvals WHERE id = ?`, id))
}

// ListApprovals returns requests, newest first.
func ListApprovals(home string, pendingOnly bool, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 50
	}
	db, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT id, session_id, call_id, server, tool, args, reason, status, created_at, decided_at, decided_by
		   FROM approvals
		  WHERE (? = 0 OR status = 'pending')
		  ORDER BY id DESC LIMIT ?`, boolInt(pendingOnly), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DecideApproval resolves one pending request. The source is recorded for the audit.
func DecideApproval(home string, id int64, approved bool, by string) error {
	db, err := openHomeDB(home)
	if err != nil {
		return err
	}
	defer db.Close()

	status := ApprovalDenied
	if approved {
		status = ApprovalApproved
	}
	result, err := db.Exec(
		`UPDATE approvals SET status = ?, decided_at = ?, decided_by = ?
		  WHERE id = ? AND status = 'pending'`,
		status, time.Now().UnixMilli(), by, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrAlreadyDecided
	}
	return nil
}

// WaitApproval polls until the request is decided or the timeout expires. It returns
// the decision, or false on timeout. A missing request is an error.
func WaitApproval(home string, id int64, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	db, err := openHomeDB(home)
	if err != nil {
		return false, err
	}
	defer db.Close()

	deadline := time.Now().Add(timeout)
	for {
		var status string
		err := db.QueryRow(`SELECT status FROM approvals WHERE id = ?`, id).Scan(&status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return false, fmt.Errorf("store: approval %d not found", id)
		case err != nil:
			return false, err
		case status == ApprovalApproved:
			return true, nil
		case status == ApprovalDenied:
			return false, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func scanApproval(row rowScanner) (*Approval, error) {
	var (
		a       Approval
		created int64
		decided int64
	)
	if err := row.Scan(&a.ID, &a.SessionID, &a.CallID, &a.Server, &a.Tool, &a.Args,
		&a.Reason, &a.Status, &created, &decided, &a.DecidedBy); err != nil {
		return nil, err
	}
	a.CreatedAt = time.UnixMilli(created).UTC()
	if decided > 0 {
		a.DecidedAt = time.UnixMilli(decided).UTC()
	}
	return &a, nil
}
