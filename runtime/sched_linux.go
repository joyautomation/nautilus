//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
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
//
// A thread moved to SCHED_FIFO is bounded first (armRTGuard): one that
// runs longer than the RLIMIT_RTTIME soft limit without blocking is moved
// back to normal scheduling, with every other priority thread, and demoted
// is told why. No priority is granted without that bound (#836).
func applySched(cpus []int, prio int, shareCPU bool, demoted func(reason string)) error {
	// Pre-flight (preflight_linux.go): what already runs real-time where
	// this thread is going. A refusal leaves the thread as it was.
	if prio > 0 {
		throttleOnce.Do(func() {
			if w := throttleWarning(procRoot); w != "" {
				slog.Warn("runtime: real-time pre-flight", "warning", w)
			}
		})
	}
	warnings, err := preflight(procRoot, os.Getpid(), cpus, prio, shareCPU)
	for _, w := range warnings {
		slog.Warn("runtime: real-time pre-flight", "cpus", cpus, "priority", prio, "warning", w)
	}
	if err != nil {
		return err
	}
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
		if err := armRTGuard(); err != nil {
			return fmt.Errorf("SCHED_FIFO priority %d: refused, because a runaway real-time thread could not be bounded: %w; the task is running at normal priority", prio, err)
		}
		param := struct{ prio int32 }{int32(prio)}
		if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, schedFIFO, uintptr(unsafe.Pointer(&param))); errno != 0 {
			if errors.Is(errno, syscall.EPERM) {
				return fmt.Errorf("SCHED_FIFO priority %d: %w — needs CAP_SYS_NICE (setcap cap_sys_nice+ep on the binary, or run as root) or an rtprio rlimit ≥ %d (ulimit -r, /etc/security/limits.conf); the task is running unpinned at normal priority", prio, errno, prio)
			}
			return fmt.Errorf("SCHED_FIFO priority %d: %w", prio, errno)
		}
		rtGuard.mu.Lock()
		rtGuard.threads[syscall.Gettid()] = demoted
		rtGuard.mu.Unlock()
	}
	return nil
}

// throttleOnce: the kernel's throttle is one setting; warn about it once.
var throttleOnce sync.Once

const (
	schedOther   = 0
	schedFIFO    = 1
	rlimitRTTime = 15 // RLIMIT_RTTIME, in microseconds
	// DefaultRTLimit is how long a real-time task thread may run without
	// blocking before it is moved to normal scheduling: far above any
	// scan's execution, far below what starves a core's other work.
	DefaultRTLimit = 200 * time.Millisecond
)

// rtGuard bounds every thread this process moves to SCHED_FIFO (#836).
// RLIMIT_RTTIME counts, per thread, real-time CPU used without a blocking
// call; past the soft limit the kernel sends SIGXCPU, past the hard limit
// SIGKILL. On SIGXCPU every registered thread goes back to SCHED_OTHER (the
// signal does not say which one ran away), so the hard limit, 2.5 × the
// soft, is only the backstop. Go ignores SIGXCPU unless asked, hence the
// handler.
var rtGuard struct {
	once    sync.Once
	err     error
	soft    time.Duration
	mu      sync.Mutex
	threads map[int]func(string) // tid → told when demoted (may be nil)
}

// rtLimit is the soft limit: NAUTILUS_RT_LIMIT_MS when set (10 ms or more),
// else DefaultRTLimit.
func rtLimit() time.Duration {
	if v := os.Getenv("NAUTILUS_RT_LIMIT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 10 {
			return time.Duration(ms) * time.Millisecond
		}
		slog.Warn("runtime: NAUTILUS_RT_LIMIT_MS is not a whole number of milliseconds ≥ 10; using the default", "value", v, "default", DefaultRTLimit)
	}
	return DefaultRTLimit
}

// rtLimits is the RLIMIT_RTTIME to set for a soft limit, given the one in
// force: a stricter soft or hard limit already set (by the launcher, say)
// is kept, and soft never exceeds hard.
func rtLimits(cur syscall.Rlimit, soft time.Duration) syscall.Rlimit {
	want := syscall.Rlimit{Cur: uint64(soft / time.Microsecond), Max: uint64(soft * 5 / 2 / time.Microsecond)}
	if cur.Cur < want.Cur {
		want.Cur = cur.Cur
	}
	if cur.Max < want.Max {
		want.Max = cur.Max
	}
	if want.Cur > want.Max {
		want.Cur = want.Max
	}
	return want
}

// armRTGuard sets the limit and the SIGXCPU handler, once per process.
func armRTGuard() error {
	rtGuard.once.Do(func() {
		// Under mu: a loop on another thread may be releasing or
		// registering already (#870).
		rtGuard.mu.Lock()
		if rtGuard.threads == nil {
			rtGuard.threads = map[int]func(string){}
		}
		rtGuard.mu.Unlock()
		var cur syscall.Rlimit
		if err := syscall.Getrlimit(rlimitRTTime, &cur); err != nil {
			rtGuard.err = fmt.Errorf("RLIMIT_RTTIME: %w", err)
			return
		}
		lim := rtLimits(cur, rtLimit())
		if err := syscall.Setrlimit(rlimitRTTime, &lim); err != nil {
			rtGuard.err = fmt.Errorf("RLIMIT_RTTIME: %w", err)
			return
		}
		rtGuard.soft = time.Duration(lim.Cur) * time.Microsecond
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGXCPU)
		go func() {
			// The signal does not say which limit it was for: RLIMIT_RTTIME
			// (ours) or RLIMIT_CPU (someone's ulimit -t). Either way the
			// process is past a CPU bound, and the real-time threads go.
			for range c {
				demoteAll(fmt.Sprintf("SIGXCPU: a CPU-time limit was passed (most likely RLIMIT_RTTIME: a real-time task thread ran %s without blocking; NAUTILUS_RT_LIMIT_MS); every task with priority: now runs at normal priority until restart", rtGuard.soft))
			}
		}()
	})
	return rtGuard.err
}

// demoteAll moves every registered real-time thread to SCHED_OTHER.
func demoteAll(reason string) {
	rtGuard.mu.Lock()
	threads := rtGuard.threads
	rtGuard.threads = map[int]func(string){}
	rtGuard.mu.Unlock()
	if len(threads) == 0 {
		return
	}
	slog.Error("runtime: real-time priority withdrawn", "reason", reason, "threads", len(threads))
	for tid, told := range threads {
		if err := setOther(tid); err != nil {
			slog.Error("runtime: could not move a real-time thread to normal scheduling", "tid", tid, "error", err)
		}
		if told != nil {
			told(reason)
		}
	}
}

func setOther(tid int) error {
	param := struct{ prio int32 }{0}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, uintptr(tid), schedOther, uintptr(unsafe.Pointer(&param))); errno != 0 {
		return errno
	}
	return nil
}

// releaseSched undoes a priority on the calling thread before its loop
// gives the thread back to Go: an unlocked thread left at SCHED_FIFO would
// run arbitrary goroutines at real-time priority.
func releaseSched() {
	tid := syscall.Gettid()
	rtGuard.mu.Lock()
	_, ok := rtGuard.threads[tid]
	delete(rtGuard.threads, tid)
	rtGuard.mu.Unlock()
	if ok {
		if err := setOther(tid); err != nil {
			slog.Error("runtime: a loop ended but its thread could not leave real-time scheduling", "tid", tid, "error", err)
		}
	}
}

func currentTid() int { return syscall.Gettid() }

// demoteTid moves one registered real-time thread to SCHED_OTHER and tells
// its task why (the priority guard's path, prioguard.go). False when tid
// holds no priority of ours: never granted, or already demoted.
func demoteTid(tid int, reason string) bool {
	if tid == 0 {
		return false
	}
	rtGuard.mu.Lock()
	told, ok := rtGuard.threads[tid]
	delete(rtGuard.threads, tid)
	rtGuard.mu.Unlock()
	if !ok {
		return false
	}
	if err := setOther(tid); err != nil {
		slog.Error("runtime: could not move a real-time thread to normal scheduling", "tid", tid, "error", err)
	}
	if told != nil {
		told(reason)
	}
	return true
}

// holdsPriority: tid is a thread this process moved to real-time priority
// and has not demoted.
func holdsPriority(tid int) bool {
	if tid == 0 {
		return false
	}
	rtGuard.mu.Lock()
	defer rtGuard.mu.Unlock()
	_, ok := rtGuard.threads[tid]
	return ok
}
