//go:build unix

package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// KeepOwner gives the new file at tmp the owner of the file it replaces,
// or, for a first file, of its directory, when this process may (root).
// A tool is run with sudo on a box whose service runs as another user
// (systemd's DynamicUser=): a file left owned by root, mode 0600, would be
// one the running controller cannot read. Lstat: a file planted as a
// symlink lends nothing (WriteAtomic refuses one anyway).
func KeepOwner(tmp *os.File, path, dir string, root bool) error {
	if !root {
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

// OthersMayRead reports a mode that lets the group or others at the file.
func OthersMayRead(m os.FileMode) bool { return m&0o077 != 0 }

// OthersMayWrite reports a directory mode that lets the group or others
// replace what is in it.
func OthersMayWrite(m os.FileMode) bool { return m&0o022 != 0 }

// CheckOwner refuses a file euid does not own: the controller reads only
// a file that is its own (root's, when it runs as root), never one another
// user put in its state directory. tool names what writes it ("naut
// users"), for the fix.
func CheckOwner(path string, st os.FileInfo, euid int, tool string) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(sys.Uid) != euid {
		return fmt.Errorf("%s is owned by uid %d, not this controller's (%d): chown it, or let %s (run with sudo) write it", path, sys.Uid, euid, tool)
	}
	return nil
}

// chownLock gives the open lock f its directory's owner, after checking
// (on the open file, not the path) that it is a plain file with one link.
func chownLock(f *os.File, path, tool string) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is not a plain file with one link: remove it (%s makes a new one)", path, tool)
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

// Lock takes an exclusive lock on path (created 0600), waiting up to wait
// for another holder (tool, named in a refusal) to finish.
func Lock(path string, root bool, wait time.Duration, tool string) (func(), error) {
	// O_NOFOLLOW: a lock planted as a symbolic link (by whoever owns the
	// state directory) is refused, never followed.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symbolic link: remove it (%s makes a new one)", path, tool)
	}
	if err != nil {
		return nil, err
	}
	// Run as root (sudo), the lock gets its directory's owner, like the
	// files it guards (KeepOwner): the controller may take it too, and must
	// be able to open it. A lock an earlier release left owned by root is
	// given back the same way. Only a plain file with one link is: root
	// must never hand the directory's owner a file it reached through a
	// hard link.
	if root {
		if err := chownLock(f, path, tool); err != nil {
			f.Close()
			return nil, err
		}
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s: another %s holds it: %w", path, tool, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
