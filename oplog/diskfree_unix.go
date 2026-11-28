//go:build unix

package oplog

import "golang.org/x/sys/unix"

// diskFree is the space free to an unprivileged writer in dir.
func diskFree(dir string) (int64, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(dir, &s); err != nil {
		return -1, err
	}
	return int64(s.Bavail) * int64(s.Bsize), nil //nolint:unconvert // field types differ by OS
}
