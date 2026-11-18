//go:build linux

package main

import (
	"syscall"
	"time"
	"unsafe"
)

// armKillTimer has the kernel send this process SIGKILL after d, from a
// POSIX timer (timer_create, CLOCK_MONOTONIC). No goroutine, thread or
// helper process has to run for it to fire, so it ends the run even when
// everything in this process is starved: the run guard's deadline is a
// goroutine, and this is its backstop (#836).
func armKillTimer(d time.Duration) error {
	// struct sigevent: sigval (pointer-sized), signo, notify, then a union
	// padding it to 64 bytes; the extra words here are never read.
	var sev struct {
		value  uintptr
		signo  int32
		notify int32 // SIGEV_SIGNAL = 0
		_      [16]int32
	}
	sev.signo = int32(syscall.SIGKILL)
	var id int32
	if _, _, e := syscall.Syscall(syscall.SYS_TIMER_CREATE, 1 /* CLOCK_MONOTONIC */, uintptr(unsafe.Pointer(&sev)), uintptr(unsafe.Pointer(&id))); e != 0 {
		return e
	}
	spec := [2]syscall.Timespec{{}, syscall.NsecToTimespec(d.Nanoseconds())} // interval 0, value d
	if _, _, e := syscall.Syscall6(syscall.SYS_TIMER_SETTIME, uintptr(id), 0, uintptr(unsafe.Pointer(&spec)), 0, 0, 0); e != 0 {
		return e
	}
	return nil
}
