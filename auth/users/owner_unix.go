//go:build unix

package users

import (
	"os"
	"syscall"
)

// keepOwner gives the new file at tmp the owner of the file it replaces,
// or, for a first file, of its directory, when this process may (root).
// naut users is run with sudo on a box whose service runs as another user
// (systemd's DynamicUser=): a file left owned by root, mode 0600, would be
// one the running controller cannot read.
func keepOwner(tmp *os.File, path, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
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

// othersMayRead reports a mode that lets the group or others at the file.
func othersMayRead(m os.FileMode) bool { return m&0o077 != 0 }
