package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Ledger remembers the approved tool definitions for each server command, so a later
// run can detect a definition that changed after it was approved.
//
// The first definition seen for a tool is trusted on first use. A later, different
// definition is recorded as pending and keeps being reported as changed until a human
// accepts it with Accept; it never silently replaces the approved one.
//
// Several Black Box processes can share one ledger file (one per wrapped server), so
// every write merges with what is on disk instead of overwriting it.
type Ledger struct {
	mu   sync.Mutex
	path string
	data ledgerData
	// cleared remembers pending entries this process saw reverted and has not yet
	// removed from disk, so a merge with the file does not bring them back. It is
	// emptied once a write has gone through: after that the file no longer has them,
	// and a pending entry with the same hash is a new change, ours or another
	// process's, that must not be erased.
	cleared map[string]string
	// dirty is set when a definition was remembered that is not on disk yet.
	dirty bool
	// lastMod is the ledger's mtime when this process last read or wrote it, used to
	// notice another process accepting a change.
	lastMod time.Time
}

type ledgerData struct {
	// Servers maps server key -> tool -> approved hash.
	Servers map[string]map[string]string `json:"servers"`
	// Pending maps server key -> tool -> the changed hash awaiting approval.
	Pending map[string]map[string]string `json:"pending,omitempty"`
	// Definitions maps hash -> the canonical definition it was computed from, so a
	// human can see what changed before accepting it. Only hashes still referenced
	// by Servers or Pending are kept.
	Definitions map[string]json.RawMessage `json:"definitions,omitempty"`
}

// PendingChange is a tool whose definition changed and has not been accepted.
type PendingChange struct {
	Server   string
	Tool     string
	Approved string
	Pending  string
	// ApprovedDef and PendingDef are the canonical definitions, when recorded.
	// Ledgers written before definitions were kept only have hashes.
	ApprovedDef json.RawMessage
	PendingDef  json.RawMessage
}

// LoadLedger reads the ledger. A missing file is a fresh ledger; a corrupt one is an
// error, because silently starting over would forget every approved tool definition.
func LoadLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, cleared: map[string]string{}}
	unlock := lockLedgerForRead(path)
	defer unlock()
	if info, err := os.Stat(path); err == nil {
		l.lastMod = info.ModTime()
	}
	d, err := readLedger(path)
	if err != nil {
		return nil, err
	}
	l.data = d
	return l, nil
}

// lockLedgerForRead takes the ledger lock for a read. Writers replace the file by
// rename while holding it; on Windows a reader's open handle makes that rename fail
// and a read during it fails, so readers must not overlap a writer.
//
// The lock is not re-entrant: callers must not already hold it. When it cannot be
// taken (no home directory yet, or a read-only one) the read goes ahead without it,
// which is no worse than not locking and keeps read-only commands working.
func lockLedgerForRead(path string) func() {
	if path == "" {
		return func() {}
	}
	unlock, err := lockLedger(path)
	if err != nil {
		return func() {}
	}
	return unlock
}

func readLedger(path string) (ledgerData, error) {
	d := ledgerData{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &d); err != nil {
			return ledgerData{}, fmt.Errorf("ledger %s is corrupt: %w (move it aside or run `blackbox tools reset`)", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// A fresh install has no ledger yet.
	default:
		return ledgerData{}, fmt.Errorf("ledger %s: %w", path, err)
	}
	if d.Servers == nil {
		d.Servers = map[string]map[string]string{}
	}
	if d.Pending == nil {
		d.Pending = map[string]map[string]string{}
	}
	if d.Definitions == nil {
		d.Definitions = map[string]json.RawMessage{}
	}
	return d, nil
}

// RememberDefinition keeps the canonical definition behind a hash. It is written with
// the next save, which Observe performs for the same tools/list.
func (l *Ledger) RememberDefinition(hash string, def []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.data.Definitions[hash]; ok || len(def) == 0 {
		return
	}
	l.data.Definitions[hash] = append(json.RawMessage(nil), def...)
	l.dirty = true
}

// Observe reports whether a definition differs from the approved one. A first
// observation is never a change; it becomes the approved definition and is persisted
// immediately, so detection works across sessions even if the process is killed.
func (l *Ledger) Observe(server, tool, hash string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refreshLocked()
	approved, seen := l.data.Servers[server][tool]
	switch {
	case !seen:
		setNested(l.data.Servers, server, tool, hash)
		deleteNested(l.data.Pending, server, tool)
	case approved == hash:
		pending, ok := l.data.Pending[server][tool]
		if !ok {
			l.flushLocked()
			return false
		}
		l.cleared[server+"\x00"+tool] = pending
		deleteNested(l.data.Pending, server, tool)
	default:
		if l.data.Pending[server][tool] == hash {
			l.flushLocked()
			return true
		}
		// The server changed again after a revert this process has not managed to
		// write yet: the newest observation wins.
		delete(l.cleared, server+"\x00"+tool)
		setNested(l.data.Pending, server, tool, hash)
	}
	l.dirty = true
	l.flushLocked()
	return seen && approved != hash
}

// flushLocked writes unsaved state. A failed write is not reported: Observe's caller
// is relaying a tools/list and has nothing useful to do with it. What was observed
// stays in memory, so this process keeps answering correctly, and the ledger stays
// dirty so the next call tries again.
func (l *Ledger) flushLocked() {
	if l.dirty {
		_ = l.saveLocked()
	}
}

// Accept approves pending changes. An empty server matches every server, and no tools
// means every pending tool of the matched servers. It returns what was accepted.
func (l *Ledger) Accept(server string, tools ...string) ([]PendingChange, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Start from the latest file so an accept never loses another process's writes.
	if l.path != "" {
		unlock, err := lockLedger(l.path)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	disk, err := readLedger(l.path)
	if err != nil {
		return nil, err
	}
	l.data = mergeLedger(disk, l.data, l.cleared)

	want := map[string]bool{}
	for _, t := range tools {
		want[t] = true
	}
	var accepted []PendingChange
	for srv, pend := range l.data.Pending {
		if server != "" && srv != server {
			continue
		}
		for tool, hash := range pend {
			if len(want) > 0 && !want[tool] {
				continue
			}
			accepted = append(accepted, l.changeLocked(srv, tool, hash))
			setNested(l.data.Servers, srv, tool, hash)
			deleteNested(l.data.Pending, srv, tool)
		}
	}
	sortChanges(accepted)
	if len(accepted) == 0 {
		return nil, nil
	}
	if err := l.writeLocked(); err != nil {
		return nil, err
	}
	l.cleared = map[string]string{}
	l.touchLocked()
	return accepted, nil
}

// PendingChanges lists definitions that changed and await approval.
func (l *Ledger) PendingChanges() []PendingChange {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refreshLocked()
	var out []PendingChange
	for srv, pend := range l.data.Pending {
		for tool, hash := range pend {
			out = append(out, l.changeLocked(srv, tool, hash))
		}
	}
	sortChanges(out)
	return out
}

// Save flushes the ledger to disk, merged with any concurrent writer's changes.
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.saveLocked()
}

// IsPending reports whether a tool has a changed definition awaiting approval.
func (l *Ledger) IsPending(server, tool string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked()
	_, ok := l.data.Pending[server][tool]
	return ok
}

func (l *Ledger) changeLocked(server, tool, pending string) PendingChange {
	approved := l.data.Servers[server][tool]
	return PendingChange{
		Server: server, Tool: tool, Approved: approved, Pending: pending,
		ApprovedDef: l.data.Definitions[approved],
		PendingDef:  l.data.Definitions[pending],
	}
}

// Known returns the approved hash for a tool, if any.
func (l *Ledger) Known(server, tool string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked()
	h, ok := l.data.Servers[server][tool]
	return h, ok
}

// refreshLocked merges the file if another process has written it since our last read.
func (l *Ledger) refreshLocked() {
	if l.path == "" {
		return
	}
	info, err := os.Stat(l.path)
	if err != nil || !info.ModTime().After(l.lastMod) {
		return
	}
	// The caller holds l.mu but never the file lock, which is not re-entrant.
	unlock := lockLedgerForRead(l.path)
	defer unlock()
	// Stat again under the lock, so the time remembered is that of the file read.
	if locked, err := os.Stat(l.path); err == nil {
		info = locked
	}
	disk, err := readLedger(l.path)
	if err != nil {
		return // keep the state we have; the next write will surface the error
	}
	l.data = mergeLedger(disk, l.data, l.cleared)
	l.lastMod = info.ModTime()
}

func (l *Ledger) touchLocked() {
	if info, err := os.Stat(l.path); err == nil {
		l.lastMod = info.ModTime()
	}
}

func (l *Ledger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	unlock, err := lockLedger(l.path)
	if err != nil {
		return err
	}
	defer unlock()

	disk, err := readLedger(l.path)
	if err != nil {
		return err
	}
	l.data = mergeLedger(disk, l.data, l.cleared)
	if err := l.writeLocked(); err != nil {
		return err
	}
	// The file now reflects every revert this process saw. Keeping the entries
	// would delete the same hash again on every later merge, including after the
	// server changed to it once more or another process recorded it as pending.
	l.cleared = map[string]string{}
	l.dirty = false
	l.touchLocked()
	return nil
}

// writeLocked replaces the file atomically, so a crash or a concurrent reader never
// sees a half-written ledger. The caller holds the ledger lock.
func (l *Ledger) writeLocked() error {
	if l.path == "" {
		return nil
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	pruneDefinitions(&l.data)
	b, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tooldefs-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// mergeLedger combines the file on disk with this process's view. Approved hashes on
// disk win, because they only change through Accept; entries this process saw first
// are added. A pending change is dropped once its hash has been approved, or when this
// process saw the server revert to the approved definition.
func mergeLedger(disk, mem ledgerData, cleared map[string]string) ledgerData {
	for srv, tools := range mem.Servers {
		for tool, hash := range tools {
			if _, ok := disk.Servers[srv][tool]; !ok {
				setNested(disk.Servers, srv, tool, hash)
			}
		}
	}
	for srv, tools := range mem.Pending {
		for tool, hash := range tools {
			if _, ok := disk.Pending[srv][tool]; !ok {
				setNested(disk.Pending, srv, tool, hash)
			}
		}
	}
	for srv, tools := range disk.Pending {
		for tool, hash := range tools {
			if disk.Servers[srv][tool] == hash || cleared[srv+"\x00"+tool] == hash {
				deleteNested(disk.Pending, srv, tool)
			}
		}
	}
	for hash, def := range mem.Definitions {
		if _, ok := disk.Definitions[hash]; !ok {
			disk.Definitions[hash] = def
		}
	}
	return disk
}

// pruneDefinitions drops definitions no approved or pending hash refers to, so the
// file does not grow with every version a server has ever advertised.
func pruneDefinitions(d *ledgerData) {
	used := map[string]bool{}
	for _, m := range []map[string]map[string]string{d.Servers, d.Pending} {
		for _, tools := range m {
			for _, hash := range tools {
				used[hash] = true
			}
		}
	}
	for hash := range d.Definitions {
		if !used[hash] {
			delete(d.Definitions, hash)
		}
	}
}

func setNested(m map[string]map[string]string, a, b, v string) {
	if m[a] == nil {
		m[a] = map[string]string{}
	}
	m[a][b] = v
}

func deleteNested(m map[string]map[string]string, a, b string) {
	if m[a] == nil {
		return
	}
	delete(m[a], b)
	if len(m[a]) == 0 {
		delete(m, a)
	}
}

func sortChanges(c []PendingChange) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Server != c[j].Server {
			return c[i].Server < c[j].Server
		}
		return c[i].Tool < c[j].Tool
	})
}
