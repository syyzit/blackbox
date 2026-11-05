//go:build !windows

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockLedger takes an exclusive advisory lock on the ledger's sidecar lock file.
// The ledger itself is replaced by rename, so the lock must live on a separate path
// that never changes inode.
func lockLedger(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
