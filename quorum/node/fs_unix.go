//go:build unix

package node

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// syncDir fsyncs a directory, so a rename in it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// lock takes an exclusive lock on path (created if missing) for this
// process's life, or fails at once if another process holds it: two
// processes that both claim one voter's state (two `naut witness` on one
// directory, a second controller started on the same box) would each
// grant from their own copy of the promise.
func lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is held by another process: one voter state, one process (%w)", path, err)
	}
	return func() { f.Close() }, nil
}
