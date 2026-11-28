//go:build windows

package oplog

import "golang.org/x/sys/windows"

// diskFree is the space free to this user in dir.
func diskFree(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1, err
	}
	var free, total, all uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &all); err != nil {
		return -1, err
	}
	return int64(free), nil
}
