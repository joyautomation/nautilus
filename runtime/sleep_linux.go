//go:build linux

package runtime

import (
	"context"
	goruntime "runtime"
	"syscall"
	"time"
	"unsafe"
)

// On Linux a scan loop sleeps with clock_nanosleep(CLOCK_MONOTONIC,
// TIMER_ABSTIME) on a thread of its own, with the thread's timer slack cut
// from the 50 µs default to 1 µs. Measured on an i9 desktop (stock kernel,
// in use) at a 1 ms period, slot lateness p50 / p99 / p99.9:
//
//	Go timer + absolute deadline      535 µs / 1.05 ms / 1.3 ms
//	clock_nanosleep, locked thread     56 µs /   88 µs / 0.6 ms
//	… and timer slack 1 ns              5 µs /   21 µs /  76 µs
//
// The Go timer's floor is the runtime's network poller, which waits in
// epoll_wait with a whole-millisecond timeout: any sleep under 1 ms takes
// at least 1 ms. Going to the kernel's own absolute sleep avoids it, and
// the slack setting is what a non-RT kernel offers for the rest. See
// docs/design/realtime.md, Phase 2 item 1.
//
// Numbers from the syscall package only (stdlib core, HANDOFF.md).

const (
	clockMonotonic  = 1
	timerAbstime    = 1
	prSetTimerslack = 29
	timerSlackNs    = 1000
	// sleepChunk bounds one uninterruptible sleep so a cancelled context
	// is noticed within it: the loop sleeps to min(deadline, now+chunk),
	// checks ctx, and goes again. The final chunk still ends on the
	// deadline exactly.
	sleepChunk = 50 * time.Millisecond
)

// loopThread pins the calling goroutine to its own OS thread for the life
// of the loop and sets the thread's timer slack. Returns the undo.
func loopThread() func() {
	goruntime.LockOSThread()
	// Failure here (an old kernel, a seccomp profile) leaves the default
	// slack: the loop still works, 50 µs later. Not worth refusing to run.
	_, _, _ = syscall.Syscall(syscall.SYS_PRCTL, prSetTimerslack, timerSlackNs, 0)
	return goruntime.UnlockOSThread
}

func monotonicNs() int64 {
	var ts syscall.Timespec
	_, _, _ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}

// sleepUntil blocks until deadline (a wall time with a monotonic reading,
// as time.Now gives) or until ctx is done, whichever is first. Returns
// false when ctx ended it.
func sleepUntil(ctx context.Context, deadline time.Time) bool {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		chunk := remaining
		if chunk > sleepChunk {
			chunk = sleepChunk
		}
		// The deadline is re-anchored to CLOCK_MONOTONIC here: the gap
		// between the two clock reads is tens of nanoseconds.
		ts := syscall.NsecToTimespec(monotonicNs() + int64(chunk))
		for {
			_, _, errno := syscall.Syscall6(syscall.SYS_CLOCK_NANOSLEEP, clockMonotonic, timerAbstime,
				uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
			if errno != syscall.EINTR {
				break
			}
		}
	}
}
