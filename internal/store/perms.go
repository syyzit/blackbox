package store

import (
	"errors"
	"io/fs"
	"os"
)

// Recordings hold tool arguments and results: file contents, command output, and
// anything a redaction rule did not recognize. They are private to the user who
// recorded them, so the home directory is created 0700 and the database files are
// kept 0600 (Windows ignores these modes and relies on the profile's ACLs).

// ensureHome creates the data directory, readable only by its owner. An existing
// directory keeps its mode, since -home may point at a directory the user manages.
func ensureHome(home string) error {
	return os.MkdirAll(home, 0o700)
}

// createPrivate creates path as an empty owner-only file if it does not exist yet, so
// the database is never briefly world-readable between creation and a chmod.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// restrictDB makes the database and its WAL and shared-memory files owner-only.
// SQLite creates the side files with the database's mode, but files left by an
// older version may still be 0644.
func restrictDB(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o600)
		}
	}
}
