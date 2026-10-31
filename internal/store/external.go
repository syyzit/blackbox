package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
)

var badExternalChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ExternalID turns an external session id into a stable Black Box session id.
func ExternalID(prefix, id string) string {
	clean := badExternalChars.ReplaceAllString(id, "_")
	if clean == "" {
		clean = "unknown"
	}
	if len(clean) > 48 {
		sum := sha256.Sum256([]byte(clean))
		clean = clean[:32] + "-" + hex.EncodeToString(sum[:4])
	}
	return prefix + clean
}

// AppendExternal appends events recorded outside the MCP proxy to a session keyed by
// an external id. The session is created on first use.
func AppendExternal(home, sessionID string, argv []string, events []event.Event) error {
	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	id := ExternalID("oc-", sessionID)
	host, _ := os.Hostname()
	now := time.Now().UTC()
	startedAt := now
	for _, e := range events {
		if !e.At.IsZero() && e.At.Before(startedAt) {
			startedAt = e.At
		}
	}

	if _, err := db.Exec(
		`INSERT OR IGNORE INTO sessions (id, started_at, argv, pid, hostname, server_key)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, startedAt.UnixMilli(), mustJSON(argv), os.Getpid(), host, ServerKey(argv),
	); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events WHERE session_id = ?`, id).Scan(&seq); err != nil {
		return err
	}
	for _, e := range events {
		seq++
		e.SessionID = id
		e.Seq = seq
		if e.At.IsZero() {
			e.At = now
		}
		if err := insertEvent(tx, e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EventsBetween returns every event recorded in a time window, across sessions.
//
// Coverage uses this so a change recorded by another source (for example an editor
// plugin) still counts as explained for the MCP session that was running then.
func EventsBetween(home string, start, end time.Time) ([]event.Event, error) {
	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT session_id, seq, at, direction, method, tool, tool_hash, rpc_id, status,
		        result_type, duration_ms, size, args, result, error, raw, usage_in,
		        usage_out, cost_usd, call_id, subscription_id
		   FROM events
		  WHERE at >= ? AND at <= ?
		  ORDER BY at, seq`, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []event.Event
	for rows.Next() {
		var (
			e       event.Event
			at      int64
			dir     string
			args    sql.NullString
			result  sql.NullString
			errText sql.NullString
			raw     sql.NullString
		)
		if err := rows.Scan(
			&e.SessionID, &e.Seq, &at, &dir, &e.Method, &e.Tool, &e.ToolHash, &e.ID,
			&e.Status, &e.ResultType, &e.DurationMS, &e.Size, &args, &result, &errText,
			&raw, &e.UsageIn, &e.UsageOut, &e.CostUSD, &e.CallID, &e.SubscriptionID,
		); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		e.Direction = event.Direction(dir)
		e.Args, e.Result, e.Error, e.Raw = args.String, result.String, errText.String, raw.String
		out = append(out, e)
	}
	return out, rows.Err()
}
