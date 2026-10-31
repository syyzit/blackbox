package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PruneOptions controls retention.
type PruneOptions struct {
	// OlderThan deletes sessions that started before now-OlderThan. Zero disables it.
	OlderThan time.Duration
	// MaxBytes keeps the database under a size budget by deleting the oldest sessions.
	// Zero disables it.
	MaxBytes int64
	// DryRun reports what would be deleted without touching anything.
	DryRun bool
	// Vacuum compacts the database after deleting, so the file actually shrinks.
	Vacuum bool
}

// PruneResult describes what was (or would be) removed.
type PruneResult struct {
	Sessions []Meta
	Sizes    map[string]int64
	Events   int64
	Bytes    int64
	Before   int64
	After    int64
	Vacuumed bool
}

type pruneCandidate struct {
	Meta
	eventCount int64
	eventBytes int64
}

// Prune applies a retention policy to the local store.
func Prune(home string, opts PruneOptions) (PruneResult, error) {
	if opts.OlderThan <= 0 && opts.MaxBytes <= 0 {
		return PruneResult{}, fmt.Errorf("store: prune needs older-than or max-size")
	}
	home = Home(home)
	dbPath := filepath.Join(home, "blackbox.db")

	db, err := openDB(dbPath)
	if err != nil {
		return PruneResult{}, err
	}
	defer db.Close()

	// Fold the WAL back into the main file so before/after sizes are comparable.
	_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	res := PruneResult{Before: dbSize(dbPath), Sizes: map[string]int64{}}

	rows, err := db.Query(
		`SELECT s.id, s.started_at, s.argv, s.pid, s.hostname, s.server_key,
		        COUNT(e.id), COALESCE(SUM(e.size), 0)
		   FROM sessions s LEFT JOIN events e ON e.session_id = s.id
		  GROUP BY s.id ORDER BY s.started_at ASC`)
	if err != nil {
		return res, err
	}

	var candidates []pruneCandidate
	for rows.Next() {
		var (
			c        pruneCandidate
			started  int64
			argvJSON string
		)
		if err := rows.Scan(&c.ID, &started, &argvJSON, &c.PID, &c.Hostname, &c.ServerKey,
			&c.eventCount, &c.eventBytes); err != nil {
			rows.Close()
			return res, err
		}
		c.StartedAt = time.UnixMilli(started).UTC()
		c.Argv = decodeArgv(argvJSON)
		c.Events = c.eventCount
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, err
	}
	rows.Close()

	picked := map[string]bool{}

	if opts.OlderThan > 0 {
		cutoff := time.Now().Add(-opts.OlderThan)
		for _, c := range candidates {
			if c.StartedAt.Before(cutoff) {
				picked[c.ID] = true
			}
		}
	}

	if opts.MaxBytes > 0 {
		var total int64
		for _, c := range candidates {
			total += c.eventBytes
		}
		for _, c := range candidates {
			if total <= opts.MaxBytes {
				break
			}
			if !picked[c.ID] {
				picked[c.ID] = true
				total -= c.eventBytes
			}
		}
	}

	var ids []string
	for _, c := range candidates {
		if picked[c.ID] {
			res.Sessions = append(res.Sessions, c.Meta)
			res.Sizes[c.ID] = c.eventBytes
			res.Events += c.eventCount
			res.Bytes += c.eventBytes
			ids = append(ids, c.ID)
		}
	}

	if opts.DryRun || len(ids) == 0 {
		res.After = res.Before
		return res, nil
	}

	if err := deleteSessions(db, ids); err != nil {
		return res, err
	}

	if opts.Vacuum {
		// VACUUM cannot run inside a transaction.
		if _, err := db.Exec("VACUUM"); err != nil {
			return res, fmt.Errorf("store: vacuum: %w", err)
		}
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		res.Vacuumed = true
	}
	res.After = dbSize(dbPath)
	return res, nil
}

func deleteSessions(db *sql.DB, ids []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete session %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func dbSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}

func decodeArgv(raw string) []string {
	var argv []string
	_ = json.Unmarshal([]byte(raw), &argv)
	return argv
}
