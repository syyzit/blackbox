// Package store persists recorded sessions.
//
// Phase 0 uses an append-only JSONL file per session: crash-safe, greppable, and
// readable with zero tooling. Phase 2 replaces the query layer with SQLite while
// keeping the same Emit contract.
package store

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
)

// Meta describes a recorded session.
type Meta struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	Argv      []string  `json:"argv"`
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname"`
	Events    int64     `json:"events"`
}

// Session is an open recording.
type Session struct {
	Meta Meta
	Dir  string

	mu     sync.Mutex
	f      *os.File
	seq    int64
	ledger *Ledger
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
	id := NewID()
	dir := filepath.Join(home, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create session dir: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open event log: %w", err)
	}

	host, _ := os.Hostname()
	meta := Meta{
		ID:        id,
		StartedAt: time.Now().UTC(),
		Argv:      argv,
		PID:       os.Getpid(),
		Hostname:  host,
	}
	if err := writeMeta(dir, meta); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Session{
		Meta:   meta,
		Dir:    dir,
		f:      f,
		ledger: LoadLedger(filepath.Join(home, "tooldefs.json")),
	}, nil
}

func writeMeta(dir string, meta Meta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("store: write meta: %w", err)
	}
	return nil
}

// Emit appends one event, assigning sequence and session id.
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

	if len(e.ToolDefs) > 0 && s.ledger != nil {
		server := ServerKey(s.Meta.Argv)
		for i := range e.ToolDefs {
			if s.ledger.Observe(server, e.ToolDefs[i].Name, e.ToolDefs[i].Hash) {
				e.ToolDefs[i].Changed = true
			}
		}
		_ = s.ledger.Save()
	}

	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.f.Write(append(b, '\n'))
	return err
}

// ServerKey fingerprints a server command so its tool definitions can be tracked
// across sessions.
func ServerKey(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// Close flushes and closes the event log.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	if err == nil {
		s.Meta.Events = s.seq
		_ = writeMeta(s.Dir, s.Meta)
	}
	return err
}

// NewID returns a sortable, unique session id.
func NewID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// List returns sessions newest first, with event counts.
func List(home string) ([]Meta, error) {
	home = Home(home)
	dirs, err := filepath.Glob(filepath.Join(home, "sessions", "*", "meta.json"))
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(dirs))
	for _, path := range dirs {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		m.Events = countLines(filepath.Join(filepath.Dir(path), "events.jsonl"))
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// Find resolves a full or unique-prefix session id.
func Find(home, prefix string) (Meta, []event.Event, error) {
	metas, err := List(home)
	if err != nil {
		return Meta{}, nil, err
	}
	var match *Meta
	for i := range metas {
		if metas[i].ID == prefix {
			match = &metas[i]
			break
		}
		if len(prefix) > 0 && len(metas[i].ID) >= len(prefix) && metas[i].ID[:len(prefix)] == prefix {
			if match != nil {
				return Meta{}, nil, fmt.Errorf("store: session prefix %q is ambiguous", prefix)
			}
			match = &metas[i]
		}
	}
	if match == nil {
		return Meta{}, nil, fmt.Errorf("store: no session matching %q", prefix)
	}
	events, err := ReadEvents(filepath.Join(Home(home), "sessions", match.ID, "events.jsonl"))
	if err != nil {
		return Meta{}, nil, err
	}
	return *match, events, nil
}

// ReadEvents loads a session's event log.
func ReadEvents(path string) ([]event.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e event.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // tolerate a torn trailing line after a crash
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func countLines(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var n int64
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		_, err := br.ReadString('\n')
		if err != nil {
			return n
		}
		n++
	}
}
