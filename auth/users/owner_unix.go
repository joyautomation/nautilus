//go:build unix

package users

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// keepOwner gives the new file at tmp the owner of the file it replaces,
// or, for a first file, of its directory, when this process may (root).
// naut users is run with sudo on a box whose service runs as another user
// (systemd's DynamicUser=): a file left owned by root, mode 0600, would be
// one the running controller cannot read. Lstat: a users.json planted as
// a symlink lends nothing (Save refuses one anyway).
func keepOwner(tmp *os.File, path, dir string) error {
	if geteuid() != 0 {
		return nil
	}
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		if st, err = os.Stat(dir); err != nil {
			return nil
		}
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return tmp.Chown(int(sys.Uid), int(sys.Gid))
}

// geteuid is os.Geteuid, a variable so a test can be another user.
var geteuid = os.Geteuid

// othersMayRead reports a mode that lets the group or others at the file.
func othersMayRead(m os.FileMode) bool { return m&0o077 != 0 }

// othersMayWrite reports a directory mode that lets the group or others
// replace what is in it.
func othersMayWrite(m os.FileMode) bool { return m&0o022 != 0 }

// checkOwner refuses a file this process does not own: the controller
// reads only a users file that is its own (root's, when it runs as root),
// never one another user put in its state directory.
func checkOwner(path string, st os.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if euid := geteuid(); int(sys.Uid) != euid {
		return fmt.Errorf("%s is owned by uid %d, not this controller's (%d): chown it, or let naut users (run with sudo) write it", path, sys.Uid, euid)
	}
	return nil
}

// lockFile takes an exclusive lock on path (created 0600), waiting for
// another naut users to finish.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	// Run as root (sudo naut users), the lock gets its directory's owner,
	// like the files it guards (keepOwner): the controller takes it too, at
	// an OIDC name's first sign-in, and must be able to open it. A lock an
	// earlier release left owned by root is given back the same way.
	if geteuid() == 0 {
		if st, err := os.Stat(filepath.Dir(path)); err == nil {
			if sys, ok := st.Sys().(*syscall.Stat_t); ok {
				f.Chown(int(sys.Uid), int(sys.Gid))
			}
		}
	}
	deadline := time.Now().Add(lockWait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s: another naut users holds it: %w", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
