package store

import (
	"time"

	"github.com/syyzit/blackbox/internal/coverage"
	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
)

// ExportFormat identifies the bundle schema.
const ExportFormat = "blackbox.session.v1"

// Bundle is a self-contained, redacted incident report for one session.
//
// It is meant to be attached to a bug report: everything needed to understand what
// the agent did, with no credentials and no dependency on the local database.
type Bundle struct {
	Format     string                  `json:"format"`
	ExportedAt time.Time               `json:"exportedAt"`
	Session    Meta                    `json:"session"`
	Stats      Stats                   `json:"stats"`
	Events     []event.Event           `json:"events"`
	Decisions  []policy.DecisionRecord `json:"decisions"`
	Coverage   *coverage.Report        `json:"coverage,omitempty"`
}

// Export builds an incident bundle for a session id or unique prefix.
func Export(home, prefix string) (Bundle, error) {
	meta, events, err := Find(home, prefix)
	if err != nil {
		return Bundle{}, err
	}
	stats, err := StatsFor(home, meta.ID)
	if err != nil {
		return Bundle{}, err
	}
	decisions, err := Decisions(home, meta.ID)
	if err != nil {
		return Bundle{}, err
	}
	if events == nil {
		events = []event.Event{}
	}
	if decisions == nil {
		decisions = []policy.DecisionRecord{}
	}
	cov, err := CoverageFor(home, meta.ID)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Format:     ExportFormat,
		ExportedAt: time.Now().UTC(),
		Session:    meta,
		Stats:      stats,
		Events:     events,
		Decisions:  decisions,
		Coverage:   cov,
	}, nil
}
