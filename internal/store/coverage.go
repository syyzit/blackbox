package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/spacecr8ed/blackbox/internal/coverage"
)

// SaveCoverage records the coverage report for this session.
func (s *Session) SaveCoverage(r coverage.Report) error {
	if r.CheckedAt.IsZero() {
		r.CheckedAt = time.Now().UTC()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO coverage (session_id, checked_at, report) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET checked_at = excluded.checked_at, report = excluded.report`,
		s.Meta.ID, r.CheckedAt.UnixMilli(), string(b),
	)
	return err
}

// CoverageFor returns a session's coverage report, or nil when none was recorded.
func CoverageFor(home, prefix string) (*coverage.Report, error) {
	db, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	id, err := resolveID(db, prefix)
	if err != nil {
		return nil, err
	}
	var raw string
	err = db.QueryRow(`SELECT report FROM coverage WHERE session_id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rep coverage.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}
