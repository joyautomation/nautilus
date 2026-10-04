//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// applySched pins the calling thread (already locked to its goroutine by
// loopThread) to cpus and, when prio > 0, moves it to SCHED_FIFO at that
// priority. Either half is skipped when unset. Errors say what to do:
// SCHED_FIFO needs CAP_SYS_NICE (or an rtprio rlimit), and the runtime
// refuses to pretend — the task runs unpinned and the error is logged and
// shown in its stats (Sched.Error) until fixed.
//
// Affinity alone does not keep OTHER threads off the core: the Go runtime,
// the kernel and every other process still schedule there unless the
// kernel was booted with isolcpus=/nohz_full=/irqaffinity= for it (see
// docs/design/realtime.md, Phase 2 item 4). It does stop the task
// migrating, which is most of what a cold cache costs.
func applySched(cpus []int, prio int) error {
	if len(cpus) > 0 {
		var mask [16]uint64 // 1024 CPUs
		for _, c := range cpus {
			if c < 0 || c >= len(mask)*64 {
				return fmt.Errorf("cpu %d: out of range", c)
			}
			mask[c/64] |= 1 << (uint(c) % 64)
		}
		if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0]))); errno != 0 {
			if errno == syscall.EINVAL {
				return fmt.Errorf("pin to cpu %v: %w (no such online CPU, or outside this process's cpuset)", cpus, errno)
			}
			return fmt.Errorf("pin to cpu %v: %w", cpus, errno)
		}
	}
	if prio > 0 {
		if prio > 99 {
			return fmt.Errorf("priority %d: SCHED_FIFO priorities are 1–99", prio)
		}
		param := struct{ prio int32 }{int32(prio)}
		const schedFIFO = 1
		if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, schedFIFO, uintptr(unsafe.Pointer(&param))); errno != 0 {
			if errors.Is(errno, syscall.EPERM) {
				return fmt.Errorf("SCHED_FIFO priority %d: %w — needs CAP_SYS_NICE (setcap cap_sys_nice+ep on the binary, or run as root) or an rtprio rlimit ≥ %d (ulimit -r, /etc/security/limits.conf); the task is running unpinned at normal priority", prio, errno, prio)
			}
			return fmt.Errorf("SCHED_FIFO priority %d: %w", prio, errno)
		}
	}
	return nil
}
