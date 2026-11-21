//go:build windows

package main

import "golang.org/x/sys/windows"

// diskUsed is the fraction of dir's volume in use (0 to 1), as the
// caller's quota sees it.
func diskUsed(dir string) (float64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}
	return 1 - float64(avail)/float64(total), nil
}
