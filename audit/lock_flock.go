//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockName is the file in a journal directory that OpenFile locks.
const LockName = ".audit.lock"

// lockDir takes an exclusive, non-blocking lock on dir's lock file. The
// kernel drops it when the process exits, so a crash leaves no stale lock.
func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("audit: the journal in %s is open in another process (another naut run in this directory?): %w", dir, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
