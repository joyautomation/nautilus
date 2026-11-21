//go:build unix

package main

import "golang.org/x/sys/unix"

// diskUsed is the fraction of dir's filesystem in use (0 to 1), as df
// counts it: what is not available to an unprivileged user.
func diskUsed(dir string) (float64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	if st.Blocks == 0 {
		return 0, nil
	}
	return 1 - float64(st.Bavail)/float64(st.Blocks), nil
}
