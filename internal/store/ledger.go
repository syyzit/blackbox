package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Ledger remembers the tool definitions seen for each server command, so a later
// run can detect a definition that changed after it was approved.
type Ledger struct {
	mu   sync.Mutex
	path string
	data ledgerData
}

type ledgerData struct {
	Servers map[string]map[string]string `json:"servers"`
}

// LoadLedger reads the ledger, tolerating a missing or corrupt file.
func LoadLedger(path string) *Ledger {
	l := &Ledger{path: path, data: ledgerData{Servers: map[string]map[string]string{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	if err := json.Unmarshal(b, &l.data); err != nil || l.data.Servers == nil {
		l.data.Servers = map[string]map[string]string{}
	}
	return l
}

// Observe records a definition hash and reports whether it changed from the
// previously recorded value. A first observation is never a change.
func (l *Ledger) Observe(server, tool, hash string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.data.Servers[server] == nil {
		l.data.Servers[server] = map[string]string{}
	}
	prev, seen := l.data.Servers[server][tool]
	l.data.Servers[server][tool] = hash
	return seen && prev != hash
}

// Save flushes the ledger to disk.
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, append(b, '\n'), 0o644)
}

// Known returns the stored hash for a tool, if any.
func (l *Ledger) Known(server, tool string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.data.Servers[server][tool]
	return h, ok
}
