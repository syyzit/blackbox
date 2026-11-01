// Package coverage detects file changes a recording did not account for.
//
// Black Box only sees MCP traffic. If an agent uses a built-in editor or shell, files
// change with no recorded write call. Coverage snapshots the tree before and after a
// session and reports the difference, so a silent blind spot becomes a visible fact.
package coverage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/syyzit/blackbox/internal/event"
)

const (
	defaultMaxFiles     = 50000
	defaultMaxFileBytes = 8 << 20
	defaultMaxHashBytes = 256 << 20
	maxReportFiles      = 200
)

// Change kinds.
const (
	Created  = "created"
	Modified = "modified"
	Deleted  = "deleted"
)

// Classification statuses.
const (
	Covered    = "covered"
	Unverified = "unverified"
	Unrecorded = "unrecorded"
)

// Options controls a snapshot.
type Options struct {
	Root         string
	MaxFiles     int
	MaxFileBytes int64
	MaxHashBytes int64
}

// File is one file's state.
type File struct {
	Size int64
	Mod  int64
	Hash string
}

// Snapshot is the state of a tree at one moment.
type Snapshot struct {
	Root      string
	Files     map[string]File
	Scanned   int
	Truncated bool
}

// Change is one path that differs between snapshots.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Class is a change plus whether the recording explains it.
type Class struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// Report is the coverage result for one session.
type Report struct {
	Root       string    `json:"root"`
	CheckedAt  time.Time `json:"checkedAt"`
	Scanned    int       `json:"scanned"`
	Truncated  bool      `json:"truncated,omitempty"`
	Changed    int       `json:"changed"`
	Covered    int       `json:"covered"`
	Unverified int       `json:"unverified"`
	Unrecorded int       `json:"unrecorded"`
	Files      []Class   `json:"files,omitempty"`
	More       int       `json:"more,omitempty"`
}

var ignoreDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "third_party": true,
	"dist": true, "build": true, "bin": true, "target": true,
	".next": true, ".nuxt": true, ".cache": true, ".turbo": true,
	".venv": true, "venv": true, "__pycache__": true,
	"DerivedData": true, ".idea": true,
}

// Take snapshots a directory.
func Take(opts Options) (*Snapshot, error) {
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	maxHashBytes := opts.MaxHashBytes
	if maxHashBytes <= 0 {
		maxHashBytes = defaultMaxHashBytes
	}

	s := &Snapshot{Root: root, Files: map[string]File{}}
	var hashed int64

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if ignoreDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets, devices
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		if len(s.Files) >= maxFiles {
			s.Truncated = true
			return fs.SkipAll
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := File{Size: info.Size(), Mod: info.ModTime().UnixNano()}
		if f.Size <= maxFileBytes && hashed < maxHashBytes {
			if h, err := hashFile(path); err == nil {
				f.Hash = h
				hashed += f.Size
			}
		}
		if f.Hash == "" {
			f.Hash = statHash(f)
		}
		s.Files[filepath.ToSlash(rel)] = f
		s.Scanned++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Diff compares two snapshots.
func Diff(a, b *Snapshot) []Change {
	var out []Change
	for path, bf := range b.Files {
		af, ok := a.Files[path]
		switch {
		case !ok:
			out = append(out, Change{Path: path, Kind: Created})
		case af.Hash != bf.Hash || af.Size != bf.Size:
			out = append(out, Change{Path: path, Kind: Modified})
		}
	}
	for path := range a.Files {
		if _, ok := b.Files[path]; !ok {
			out = append(out, Change{Path: path, Kind: Deleted})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Classify decides, for each change, whether a recorded call plausibly explains it.
//
// A path named in a write-ish or shell-ish tool call is covered. If the session had a
// recorded shell call but the path was not named, the change is unverified rather than
// unrecorded, because a shell can write anything. Everything else is unrecorded.
func Classify(root string, changes []Change, events []event.Event) Report {
	rep := Report{Root: root, Changed: len(changes)}

	var writeCalls, shellCalls []event.Event
	for _, e := range events {
		if e.Direction != event.ClientToServer || e.Method != "tools/call" || e.Status == event.StatusDenied {
			continue
		}
		name := strings.ToLower(e.Tool)
		if isShellTool(name) {
			shellCalls = append(shellCalls, e)
		}
		if isWriteTool(name) {
			writeCalls = append(writeCalls, e)
		}
	}

	for _, ch := range changes {
		abs := filepath.Join(root, ch.Path)
		status := Unrecorded
		switch {
		case namesPath(writeCalls, ch.Path, abs) || namesPath(shellCalls, ch.Path, abs):
			status = Covered
		case len(shellCalls) > 0:
			status = Unverified
		}
		switch status {
		case Covered:
			rep.Covered++
		case Unverified:
			rep.Unverified++
		default:
			rep.Unrecorded++
		}
		rep.Files = append(rep.Files, Class{Path: ch.Path, Kind: ch.Kind, Status: status})
	}

	// Most interesting first.
	rank := map[string]int{Unrecorded: 0, Unverified: 1, Covered: 2}
	sort.SliceStable(rep.Files, func(i, j int) bool {
		if rank[rep.Files[i].Status] != rank[rep.Files[j].Status] {
			return rank[rep.Files[i].Status] < rank[rep.Files[j].Status]
		}
		return rep.Files[i].Path < rep.Files[j].Path
	})
	if len(rep.Files) > maxReportFiles {
		rep.More = len(rep.Files) - maxReportFiles
		rep.Files = rep.Files[:maxReportFiles]
	}
	return rep
}

func namesPath(calls []event.Event, rel, abs string) bool {
	rel = filepath.ToSlash(rel)
	for _, e := range calls {
		for _, field := range []string{e.Args, e.Result, e.Raw} {
			if field == "" {
				continue
			}
			if strings.Contains(field, rel) || strings.Contains(field, abs) {
				return true
			}
		}
	}
	return false
}

func isWriteTool(name string) bool {
	for _, hint := range []string{
		"write", "edit", "create", "move", "rename", "delete", "remove",
		"patch", "append", "save", "mkdir", "copy", "touch", "truncate",
		"chmod", "upload", "put_", "replace",
	} {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

func isShellTool(name string) bool {
	switch name {
	case "run_command", "shell", "bash", "sh", "exec", "execute", "terminal", "run":
		return true
	}
	return false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func statHash(f File) string {
	return fmt.Sprintf("%d:%d", f.Size, f.Mod)
}
