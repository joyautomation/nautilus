//go:build unix

package users

import (
	"errors"
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

// chownLock gives the open lock f its directory's owner, after checking
// (on the open file, not the path) that it is a plain file with one link.
func chownLock(f *os.File, path string) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is not a plain file with one link: remove it (naut users makes a new one)", path)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil
	}
	if sys, ok := dir.Sys().(*syscall.Stat_t); ok && (st.Uid != sys.Uid || st.Gid != sys.Gid) {
		return f.Chown(int(sys.Uid), int(sys.Gid))
	}
	return nil
}

// lockFile takes an exclusive lock on path (created 0600), waiting for
// another naut users to finish.
func lockFile(path string) (func(), error) {
	// O_NOFOLLOW: a lock planted as a symbolic link (by whoever owns the
	// state directory) is refused, never followed.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symbolic link: remove it (naut users makes a new one)", path)
	}
	if err != nil {
		return nil, err
	}
	// Run as root (sudo naut users), the lock gets its directory's owner,
	// like the files it guards (keepOwner): the controller takes it too, at
	// an OIDC name's first sign-in, and must be able to open it. A lock an
	// earlier release left owned by root is given back the same way. Only
	// a plain file with one link is: root must never hand the directory's
	// owner a file it reached through a hard link.
	if geteuid() == 0 {
		if err := chownLock(f, path); err != nil {
			f.Close()
			return nil, err
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
