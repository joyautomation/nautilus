//go:build unix

package main

import (
	"io/fs"
	"os"
	"syscall"
)

// keepOwner gives f the owner and group of the file it replaces (st):
// naut auth init or naut opcua trust --as run under sudo must not leave a
// person's nautilus.yaml owned by root.
func keepOwner(f *os.File, st fs.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	mine, err := f.Stat()
	if err != nil {
		return err
	}
	if m, ok := mine.Sys().(*syscall.Stat_t); ok && m.Uid == sys.Uid && m.Gid == sys.Gid {
		return nil
	}
	return f.Chown(int(sys.Uid), int(sys.Gid))
}
