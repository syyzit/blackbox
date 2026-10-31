package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/policy"
)

// Trace is one agent session merged across every recorder source.
type Trace struct {
	ExternalID string                  `json:"externalId"`
	Events     []event.Event           `json:"events"`
	Sessions   []Meta                  `json:"sessions"`
	Decisions  []policy.DecisionRecord `json:"decisions"`
	StartedAt  time.Time               `json:"startedAt"`
	EndedAt    time.Time               `json:"endedAt"`
}

// TraceSummary is a row in the trace list.
type TraceSummary struct {
	ExternalID string    `json:"externalId"`
	Events     int64     `json:"events"`
	Sessions   int64     `json:"sessions"`
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt"`
}

// TraceFor merges MCP and externally recorded frames that share an agent session id.
func TraceFor(home, prefix string) (*Trace, error) {
	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	external, err := resolveExternalID(db, prefix)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(
		`SELECT session_id, seq, at, direction, method, tool, tool_hash, rpc_id, status,
		        result_type, duration_ms, size, args, result, error, raw, usage_in,
		        usage_out, cost_usd, call_id, subscription_id, external_id
		   FROM events
		  WHERE external_id = ?
		  ORDER BY at, seq`, external)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tr := &Trace{ExternalID: external}
	sessionIDs := map[string]bool{}
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
			&e.ExternalID,
		); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		e.Direction = event.Direction(dir)
		e.Args, e.Result, e.Error, e.Raw = args.String, result.String, errText.String, raw.String
		tr.Events = append(tr.Events, e)
		sessionIDs[e.SessionID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tr.Events) == 0 {
		return nil, fmt.Errorf("store: no trace matching %q", prefix)
	}
	tr.StartedAt = tr.Events[0].At
	tr.EndedAt = tr.Events[len(tr.Events)-1].At

	metas, err := List(home)
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if sessionIDs[m.ID] {
			tr.Sessions = append(tr.Sessions, m)
		}
	}

	for id := range sessionIDs {
		decisions, err := Decisions(home, id)
		if err != nil {
			continue
		}
		tr.Decisions = append(tr.Decisions, decisions...)
	}
	sort.Slice(tr.Decisions, func(i, j int) bool { return tr.Decisions[i].At.Before(tr.Decisions[j].At) })
	return tr, nil
}

// TraceList returns recent agent sessions with their merged event counts.
func TraceList(home string, limit int) ([]TraceSummary, error) {
	if limit <= 0 {
		limit = 20
	}
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
		`SELECT external_id, COUNT(*), COUNT(DISTINCT session_id), MIN(at), MAX(at)
		   FROM events
		  WHERE external_id != ''
		  GROUP BY external_id
		  ORDER BY MAX(at) DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TraceSummary
	for rows.Next() {
		var (
			t       TraceSummary
			started int64
			ended   int64
		)
		if err := rows.Scan(&t.ExternalID, &t.Events, &t.Sessions, &started, &ended); err != nil {
			return nil, err
		}
		t.StartedAt = time.UnixMilli(started).UTC()
		t.EndedAt = time.UnixMilli(ended).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

func resolveExternalID(db *sql.DB, prefix string) (string, error) {
	if prefix == "" {
		return "", errors.New("store: empty trace id")
	}
	rows, err := db.Query(
		`SELECT DISTINCT external_id FROM events
		  WHERE external_id = ? OR external_id LIKE ? LIMIT 2`,
		prefix, prefix+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("store: no trace matching %q", prefix)
	case 1:
		return ids[0], nil
	default:
		for _, id := range ids {
			if id == prefix {
				return id, nil
			}
		}
		return "", fmt.Errorf("store: trace prefix %q is ambiguous", prefix)
	}
}
