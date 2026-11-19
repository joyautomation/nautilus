//go:build linux

package runtime

import (
	"syscall"
	"time"
	"unsafe"
)

// clockBoottime is CLOCK_BOOTTIME's id (linux/time.h); the syscall package
// does not name it.
const clockBoottime = 7

// boottime reads CLOCK_BOOTTIME: monotonic, counting time the machine was
// suspended, and untouched by a wall-clock step.
func boottime() (time.Duration, bool) {
	var ts syscall.Timespec
	if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockBoottime, uintptr(unsafe.Pointer(&ts)), 0); errno != 0 {
		return 0, false
	}
	return time.Duration(ts.Nano()), true
}

// bootClock returns CLOCK_BOOTTIME's reader. Substitutes are timed on it
// (#707). The source is chosen once: if the kernel refused the clock at
// start (it has had it since 2.6.39), the whole process uses Go's
// monotonic clock instead, so two readings never mix bases.
func bootClock() func() time.Duration {
	if _, ok := boottime(); !ok {
		return goMonoClock()
	}
	return func() time.Duration {
		d, _ := boottime()
		return d
	}
}
