// Package store persists recorded sessions in SQLite.
//
// SQLite (pure-Go modernc driver, WAL mode) keeps the binary self-contained while
// giving the UI indexed queries over events, tools, and policy decisions.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/policy"
)

// Meta describes a recorded session.
type Meta struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	Argv      []string  `json:"argv"`
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname"`
	ServerKey string    `json:"serverKey"`
	Events    int64     `json:"events"`
}

// Session is an open recording backed by SQLite.
type Session struct {
	Meta Meta
	Home string

	db     *sql.DB
	ledger *Ledger

	mu  sync.Mutex
	seq int64
}

// Home resolves the Black Box data directory.
func Home(override string) string {
	if override != "" {
		return override
	}
	if v := os.Getenv("BLACKBOX_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".blackbox"
	}
	return filepath.Join(h, ".blackbox")
}

// Open starts a new session under home.
func Open(home string, argv []string) (*Session, error) {
	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("store: create home: %w", err)
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	meta := Meta{
		ID:        NewID(),
		StartedAt: time.Now().UTC(),
		Argv:      argv,
		PID:       os.Getpid(),
		Hostname:  host,
		ServerKey: ServerKey(argv),
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, started_at, argv, pid, hostname, server_key) VALUES (?, ?, ?, ?, ?, ?)`,
		meta.ID, meta.StartedAt.UnixMilli(), mustJSON(argv), meta.PID, meta.Hostname, meta.ServerKey,
	); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: insert session: %w", err)
	}

	return &Session{
		Meta:   meta,
		Home:   home,
		db:     db,
		ledger: LoadLedger(filepath.Join(home, "tooldefs.json")),
	}, nil
}

// Emit persists one event, assigning sequence and session id.
func (s *Session) Emit(e event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.Seq == 0 {
		s.seq++
		e.Seq = s.seq
	} else {
		s.seq = e.Seq
	}
	e.SessionID = s.Meta.ID

	if len(e.ToolDefs) > 0 {
		for i := range e.ToolDefs {
			if _, err := s.db.Exec(
				`INSERT INTO tool_defs (session_id, name, hash, changed) VALUES (?, ?, ?, ?)
				 ON CONFLICT(session_id, name) DO UPDATE SET hash = excluded.hash, changed = excluded.changed`,
				e.SessionID, e.ToolDefs[i].Name, e.ToolDefs[i].Hash, boolInt(e.ToolDefs[i].Changed),
			); err != nil {
				return fmt.Errorf("store: insert tool def: %w", err)
			}
		}
	}

	_, err := s.db.Exec(
		`INSERT INTO events
		 (session_id, seq, at, direction, method, tool, tool_hash, rpc_id, status, result_type,
		  duration_ms, size, args, result, error, raw, usage_in, usage_out, cost_usd,
		  call_id, subscription_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SessionID, e.Seq, e.At.UnixMilli(), string(e.Direction), e.Method, e.Tool, e.ToolHash, e.ID,
		e.Status, e.ResultType, e.DurationMS, e.Size, e.Args, e.Result, e.Error, e.Raw,
		e.UsageIn, e.UsageOut, e.CostUSD, e.CallID, e.SubscriptionID,
	)
	if err != nil {
		return fmt.Errorf("store: insert event: %w", err)
	}
	return nil
}

// Ledger exposes the cross-session tool-definition ledger.
func (s *Session) Ledger() *Ledger { return s.ledger }

// Record persists a policy decision for the audit log.
func (s *Session) Record(d policy.DecisionRecord) error {
	at := d.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO policy_decisions (session_id, at, server, tool, args, action, approved, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Meta.ID, at.UnixMilli(), d.Server, d.Tool, d.Args, string(d.Action), boolInt(d.Approved), d.Reason,
	)
	if err != nil {
		return fmt.Errorf("store: record decision: %w", err)
	}
	return nil
}

// Decisions returns the audit log for a session.
func Decisions(home, prefix string) ([]policy.DecisionRecord, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	id, err := resolveID(db, prefix)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT at, server, tool, args, action, approved, reason
		   FROM policy_decisions WHERE session_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []policy.DecisionRecord{}
	for rows.Next() {
		var (
			d        policy.DecisionRecord
			at       int64
			action   string
			approved int
		)
		if err := rows.Scan(&at, &d.Server, &d.Tool, &d.Args, &action, &approved, &d.Reason); err != nil {
			return nil, err
		}
		d.SessionID = id
		d.At = time.UnixMilli(at).UTC()
		d.Action = policy.Action(action)
		d.Approved = approved != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// Close releases the database handle.
func (s *Session) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// List returns sessions newest first, with event counts.
func List(home string) ([]Meta, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT s.id, s.started_at, s.argv, s.pid, s.hostname, s.server_key,
		        (SELECT COUNT(*) FROM events e WHERE e.session_id = s.id)
		   FROM sessions s
		  ORDER BY s.started_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Meta
	for rows.Next() {
		m, err := scanMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Find resolves a full or unique-prefix session id and returns its events.
func Find(home, prefix string) (Meta, []event.Event, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return Meta{}, nil, err
	}
	defer db.Close()

	id, err := resolveID(db, prefix)
	if err != nil {
		return Meta{}, nil, err
	}

	row := db.QueryRow(
		`SELECT s.id, s.started_at, s.argv, s.pid, s.hostname, s.server_key,
		        (SELECT COUNT(*) FROM events e WHERE e.session_id = s.id)
		   FROM sessions s WHERE s.id = ?`, id)
	meta, err := scanMeta(row)
	if err != nil {
		return Meta{}, nil, err
	}

	events, err := loadEvents(db, id)
	if err != nil {
		return Meta{}, nil, err
	}
	return meta, events, nil
}

func loadEvents(db *sql.DB, sessionID string) ([]event.Event, error) {
	rows, err := db.Query(
		`SELECT seq, at, direction, method, tool, tool_hash, rpc_id, status, result_type,
		        duration_ms, size, args, result, error, raw, usage_in, usage_out, cost_usd,
		        call_id, subscription_id
		   FROM events WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []event.Event
	for rows.Next() {
		var (
			e                          event.Event
			at                         int64
			dir                        string
			args, result, errText, raw sql.NullString
		)
		if err := rows.Scan(
			&e.Seq, &at, &dir, &e.Method, &e.Tool, &e.ToolHash, &e.ID, &e.Status, &e.ResultType,
			&e.DurationMS, &e.Size, &args, &result, &errText, &raw,
			&e.UsageIn, &e.UsageOut, &e.CostUSD, &e.CallID, &e.SubscriptionID,
		); err != nil {
			return nil, err
		}
		e.SessionID = sessionID
		e.At = time.UnixMilli(at).UTC()
		e.Direction = event.Direction(dir)
		e.Args, e.Result, e.Error, e.Raw = args.String, result.String, errText.String, raw.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach tool definitions to their tools/list responses.
	defs, err := loadToolDefs(db, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Method == "tools/list" && out[i].Direction == event.ServerToClient {
			out[i].ToolDefs = defs
		}
	}
	return out, nil
}

func loadToolDefs(db *sql.DB, sessionID string) ([]event.ToolDef, error) {
	rows, err := db.Query(`SELECT name, hash, changed FROM tool_defs WHERE session_id = ? ORDER BY name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []event.ToolDef
	for rows.Next() {
		var (
			d       event.ToolDef
			changed int
		)
		if err := rows.Scan(&d.Name, &d.Hash, &changed); err != nil {
			return nil, err
		}
		d.Changed = changed != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// Stats aggregates a session's responses.
type Stats struct {
	Calls   int        `json:"calls"`
	Errors  int        `json:"errors"`
	TotalMS int64      `json:"totalMs"`
	AvgMS   float64    `json:"avgMs"`
	MaxMS   int64      `json:"maxMs"`
	Bytes   int64      `json:"bytes"`
	CostUSD float64    `json:"costUsd"`
	Tools   []ToolStat `json:"tools"`
}

// ToolStat is per-tool aggregation.
type ToolStat struct {
	Tool   string  `json:"tool"`
	Calls  int     `json:"calls"`
	Errors int     `json:"errors"`
	AvgMS  float64 `json:"avgMs"`
	MaxMS  int64   `json:"maxMs"`
}

// Stats returns aggregate metrics for a session.
func StatsFor(home, prefix string) (Stats, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return Stats{}, err
	}
	defer db.Close()

	id, err := resolveID(db, prefix)
	if err != nil {
		return Stats{}, err
	}

	var (
		avg sql.NullFloat64
		s   Stats
	)
	err = db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status IN ('error','tool_error') THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(duration_ms), 0),
		        AVG(duration_ms),
		        COALESCE(MAX(duration_ms), 0),
		        COALESCE(SUM(size), 0),
		        COALESCE(SUM(cost_usd), 0)
		   FROM events WHERE session_id = ? AND direction = 's2c'`, id,
	).Scan(&s.Calls, &s.Errors, &s.TotalMS, &avg, &s.MaxMS, &s.Bytes, &s.CostUSD)
	if err != nil {
		return Stats{}, err
	}
	s.AvgMS = avg.Float64

	rows, err := db.Query(
		`SELECT tool, COUNT(*),
		        COALESCE(SUM(CASE WHEN status IN ('error','tool_error') THEN 1 ELSE 0 END), 0),
		        AVG(duration_ms), COALESCE(MAX(duration_ms), 0)
		   FROM events
		  WHERE session_id = ? AND direction = 's2c' AND tool != ''
		  GROUP BY tool ORDER BY COUNT(*) DESC`, id)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ts  ToolStat
			tav sql.NullFloat64
		)
		if err := rows.Scan(&ts.Tool, &ts.Calls, &ts.Errors, &tav, &ts.MaxMS); err != nil {
			return Stats{}, err
		}
		ts.AvgMS = tav.Float64
		s.Tools = append(s.Tools, ts)
	}
	return s, rows.Err()
}

// NewID returns a sortable, unique session id.
func NewID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// ServerKey fingerprints a server command so its tool definitions can be tracked
// across sessions.
func ServerKey(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

func openHomeDB(home string) (*sql.DB, error) {
	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	return openDB(filepath.Join(home, "blackbox.db"))
}

func openDB(path string) (*sql.DB, error) {
	// Per-connection pragmas must travel in the DSN: the pool creates connections
	// lazily, and a PRAGMA executed once only applies to that one connection.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open db: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id          TEXT PRIMARY KEY,
  started_at  INTEGER NOT NULL,
  argv        TEXT NOT NULL,
  pid         INTEGER NOT NULL DEFAULT 0,
  hostname    TEXT NOT NULL DEFAULT '',
  server_key  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq          INTEGER NOT NULL,
  at           INTEGER NOT NULL,
  direction    TEXT NOT NULL,
  method       TEXT NOT NULL DEFAULT '',
  tool         TEXT NOT NULL DEFAULT '',
  tool_hash    TEXT NOT NULL DEFAULT '',
  call_id      TEXT NOT NULL DEFAULT '',
  subscription_id TEXT NOT NULL DEFAULT '',
  rpc_id       TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT '',
  result_type  TEXT NOT NULL DEFAULT '',
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  size         INTEGER NOT NULL DEFAULT 0,
  args         TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  raw          TEXT NOT NULL DEFAULT '',
  usage_in     INTEGER NOT NULL DEFAULT 0,
  usage_out    INTEGER NOT NULL DEFAULT 0,
  cost_usd     REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_events_session_seq ON events(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_tool ON events(session_id, tool);

CREATE TABLE IF NOT EXISTS policy_decisions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  at          INTEGER NOT NULL,
  server      TEXT NOT NULL DEFAULT '',
  tool        TEXT NOT NULL DEFAULT '',
  args        TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL DEFAULT '',
  approved    INTEGER NOT NULL DEFAULT 0,
  reason      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_decisions_session ON policy_decisions(session_id);

CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
  method, tool, args, result, error,
  content='events', content_rowid='id', tokenize='unicode61'
);



CREATE TABLE IF NOT EXISTS tool_defs (
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  hash        TEXT NOT NULL,
  changed     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, name)
);
`

func migrate(db *sql.DB) error {
	var hadFTS int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events_fts'`).Scan(&hadFTS); err != nil {
		return err
	}

	for _, stmt := range strings.Split(schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	// Trigger bodies contain semicolons, so they cannot be split like the DDL above.
	for _, stmt := range eventTriggers {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("store: migrate trigger: %w", err)
		}
	}

	// Index rows that predate the search table. COUNT(*) on an external-content FTS
	// table reads the content table, so presence of the table is the reliable signal.
	if hadFTS == 0 {
		var events int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
			return err
		}
		if events > 0 {
			if _, err := db.Exec(`INSERT INTO events_fts(events_fts) VALUES('rebuild')`); err != nil {
				return fmt.Errorf("store: rebuild search index: %w", err)
			}
		}
	}

	// Additive migrations for databases created by an older build.
	for _, add := range []struct {
		table, column, ddl string
	}{
		{"events", "tool_hash", `ALTER TABLE events ADD COLUMN tool_hash TEXT NOT NULL DEFAULT ''`},
		{"events", "usage_in", `ALTER TABLE events ADD COLUMN usage_in INTEGER NOT NULL DEFAULT 0`},
		{"events", "usage_out", `ALTER TABLE events ADD COLUMN usage_out INTEGER NOT NULL DEFAULT 0`},
		{"events", "cost_usd", `ALTER TABLE events ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`},
		{"events", "call_id", `ALTER TABLE events ADD COLUMN call_id TEXT NOT NULL DEFAULT ''`},
		{"events", "subscription_id", `ALTER TABLE events ADD COLUMN subscription_id TEXT NOT NULL DEFAULT ''`},
	} {
		ok, err := hasColumn(db, add.table, add.column)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec(add.ddl); err != nil {
				return fmt.Errorf("store: add %s.%s: %w", add.table, add.column, err)
			}
		}
	}
	return nil
}

var eventTriggers = []string{
	`CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
	   INSERT INTO events_fts (rowid, method, tool, args, result, error)
	   VALUES (new.id, new.method, new.tool, new.args, new.result, new.error);
	 END`,
	`CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
	   INSERT INTO events_fts (events_fts, rowid, method, tool, args, result, error)
	   VALUES ('delete', old.id, old.method, old.tool, old.args, old.result, old.error);
	 END`,
	`CREATE TRIGGER IF NOT EXISTS events_au AFTER UPDATE ON events BEGIN
	   INSERT INTO events_fts (events_fts, rowid, method, tool, args, result, error)
	   VALUES ('delete', old.id, old.method, old.tool, old.args, old.result, old.error);
	   INSERT INTO events_fts (rowid, method, tool, args, result, error)
	   VALUES (new.id, new.method, new.tool, new.args, new.result, new.error);
	 END`,
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func resolveID(db *sql.DB, prefix string) (string, error) {
	if prefix == "" {
		return "", errors.New("store: empty session id")
	}
	rows, err := db.Query(`SELECT id FROM sessions WHERE id = ? OR id LIKE ? LIMIT 2`, prefix, prefix+"%")
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
		return "", fmt.Errorf("store: no session matching %q", prefix)
	case 1:
		return ids[0], nil
	default:
		for _, id := range ids {
			if id == prefix {
				return id, nil
			}
		}
		return "", fmt.Errorf("store: session prefix %q is ambiguous", prefix)
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMeta(row rowScanner) (Meta, error) {
	var (
		m        Meta
		started  int64
		argvJSON string
	)
	if err := row.Scan(&m.ID, &started, &argvJSON, &m.PID, &m.Hostname, &m.ServerKey, &m.Events); err != nil {
		return Meta{}, err
	}
	m.StartedAt = time.UnixMilli(started).UTC()
	_ = json.Unmarshal([]byte(argvJSON), &m.Argv)
	return m, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
