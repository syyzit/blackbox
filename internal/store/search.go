package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/event"
)

// SearchOptions filters a full-text search over recorded frames.
type SearchOptions struct {
	Query     string
	Since     time.Duration
	Tool      string
	Status    string
	SessionID string
	Limit     int
}

// SearchHit is one matching frame with its session.
type SearchHit struct {
	Session Meta
	Event   event.Event
}

// Search runs an FTS5 query over method, tool, args, result, and error.
func Search(home string, opts SearchOptions) ([]SearchHit, error) {
	match := ftsQuery(opts.Query)
	if match == "" {
		return nil, fmt.Errorf("store: empty search query")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
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

	var cutoff int64
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since).UnixMilli()
	}
	prefix := ""
	if opts.SessionID != "" {
		prefix = opts.SessionID + "%"
	}

	rows, err := db.Query(
		`SELECT e.session_id, e.seq, e.at, e.direction, e.method, e.tool, e.tool_hash,
		        e.rpc_id, e.status, e.result_type, e.duration_ms, e.size, e.args, e.result,
		        e.error, e.raw, e.usage_in, e.usage_out, e.cost_usd, e.call_id, e.subscription_id,
		        e.external_id,
		        s.id, s.started_at, s.argv, s.pid, s.hostname, s.server_key
		   FROM events_fts f
		   JOIN events e ON e.id = f.rowid
		   JOIN sessions s ON s.id = e.session_id
		  WHERE events_fts MATCH ?
		    AND (? = '' OR e.tool = ?)
		    AND (? = '' OR e.status = ?)
		    AND (? = 0 OR e.at >= ?)
		    AND (? = '' OR s.id LIKE ?)
		  ORDER BY e.at DESC
		  LIMIT ?`,
		match,
		opts.Tool, opts.Tool,
		opts.Status, opts.Status,
		cutoff, cutoff,
		prefix, prefix,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var (
			h        SearchHit
			at       int64
			started  int64
			dir      string
			argvJSON string
			args     sql.NullString
			result   sql.NullString
			errText  sql.NullString
			raw      sql.NullString
		)
		if err := rows.Scan(
			&h.Event.SessionID, &h.Event.Seq, &at, &dir, &h.Event.Method, &h.Event.Tool,
			&h.Event.ToolHash, &h.Event.ID, &h.Event.Status, &h.Event.ResultType,
			&h.Event.DurationMS, &h.Event.Size, &args, &result, &errText, &raw,
			&h.Event.UsageIn, &h.Event.UsageOut, &h.Event.CostUSD, &h.Event.CallID,
			&h.Event.SubscriptionID, &h.Event.ExternalID,
			&h.Session.ID, &started, &argvJSON, &h.Session.PID, &h.Session.Hostname, &h.Session.ServerKey,
		); err != nil {
			return nil, err
		}
		h.Event.At = time.UnixMilli(at).UTC()
		h.Event.Direction = event.Direction(dir)
		h.Event.Args = args.String
		h.Event.Result = result.String
		h.Event.Error = errText.String
		h.Event.Raw = raw.String
		h.Session.StartedAt = time.UnixMilli(started).UTC()
		h.Session.Argv = decodeArgv(argvJSON)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// ftsQuery turns free text into a quoted prefix query, so punctuation in tools,
// paths, and JSON cannot be parsed as FTS operators.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		if f == "" {
			continue
		}
		parts = append(parts, `"`+f+`"*`)
	}
	return strings.Join(parts, " ")
}

// SearchSnippet returns a short window around the query in the frame's text.
func SearchSnippet(e event.Event, query string) string {
	for _, field := range []string{e.Error, e.Result, e.Args, e.Raw} {
		if field == "" {
			continue
		}
		idx := strings.Index(strings.ToLower(field), strings.ToLower(strings.TrimSpace(query)))
		if idx < 0 {
			continue
		}
		start := idx - 40
		if start < 0 {
			start = 0
		}
		end := idx + 80
		if end > len(field) {
			end = len(field)
		}
		out := field[start:end]
		if start > 0 {
			out = "…" + out
		}
		if end < len(field) {
			out += "…"
		}
		return strings.ReplaceAll(out, "\n", " ")
	}
	if e.Result != "" {
		return e.Result
	}
	return e.Args
}
