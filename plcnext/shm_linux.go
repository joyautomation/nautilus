//go:build linux

package plcnext

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openShmPage maps the page read-write. It never creates it: the shim
// does, as the firmware's user, and a missing page means no shim.
func openShmPage(path string) (*shmPage, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("plcnext: shm: %w (is rt/plcnext-io/nautilus_io deployed and the runtime restarted?)", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("plcnext: shm: %w", err)
	}
	if fi.Size() < shmSize {
		return nil, fmt.Errorf("plcnext: shm: %s is %d bytes, want %d", path, fi.Size(), shmSize)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, shmSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("plcnext: shm: mmap %s: %w", path, err)
	}
	return &shmPage{mem: mem, unmap: func() error { return syscall.Munmap(mem) }}, nil
}

// monoNow is CLOCK_MONOTONIC in nanoseconds — the clock the shim stamps
// the page with. Go's own monotonic reading is not exposed as a number.
func monoNow() int64 {
	var ts syscall.Timespec
	_, _, _ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1 /* CLOCK_MONOTONIC */, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}
