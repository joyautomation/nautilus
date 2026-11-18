// rtguard.h: take SCHED_FIFO only with a bound on a runaway (#836).
//
// The C half of what the nautilus runtime does in Go (runtime/sched_linux.go):
// before the calling thread is raised to SCHED_FIFO, the process gets
// RLIMIT_RTTIME, real-time CPU a thread may use without blocking. Past the
// soft limit the kernel sends SIGXCPU; past the hard limit (2.5 times),
// SIGKILL. The default action for SIGXCPU is to die, and for the shim a
// death is the PLCnext runtime's problem too, so the handler here moves the
// guarded thread back to SCHED_OTHER instead and raises rtg_demoted for the
// cycle to report. The hard limit stays as the backstop.
//
// A thread that cannot be bounded does not get the priority: rtg_take_fifo
// refuses rather than run a real-time thread nothing would stop.
//
// Header-only, so nautilus_io.c and its test harness (ctest/) share one copy.
#ifndef RTGUARD_H
#define RTGUARD_H

#include <errno.h>
#include <sched.h>
#include <signal.h>
#include <stdint.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

#ifndef RLIMIT_RTTIME
#define RLIMIT_RTTIME 15
#endif

static volatile pid_t rtg_tid;              // the thread SIGXCPU demotes
static volatile sig_atomic_t rtg_demoted;   // set once it has been

// On SIGXCPU: the guarded thread to SCHED_OTHER, by its tid (the signal may
// land on any thread of the process). A bare syscall: async-signal-safe.
static void rtg_on_xcpu(int sig) {
    (void)sig;
    int saved = errno;
    struct sched_param p = { .sched_priority = 0 };
    if (rtg_tid > 0) syscall(SYS_sched_setscheduler, (long)rtg_tid, (long)SCHED_OTHER, &p);
    rtg_demoted = 1;
    errno = saved;
}

// rtg_arm sets RLIMIT_RTTIME to ms (hard 2.5 times; a stricter limit in
// force is kept) and the SIGXCPU handler. 0, or the errno that stopped it.
static int rtg_arm(uint64_t ms) {
    struct rlimit cur, want = { (rlim_t)(ms * 1000), (rlim_t)(ms * 2500) };
    if (getrlimit(RLIMIT_RTTIME, &cur) == 0) {
        if (cur.rlim_cur < want.rlim_cur) want.rlim_cur = cur.rlim_cur;
        if (cur.rlim_max < want.rlim_max) want.rlim_max = cur.rlim_max;
        if (want.rlim_cur > want.rlim_max) want.rlim_cur = want.rlim_max;
    }
    if (setrlimit(RLIMIT_RTTIME, &want) != 0) return errno;
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = rtg_on_xcpu;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    if (sigaction(SIGXCPU, &sa, NULL) != 0) return errno;
    return 0;
}

// rtg_take_fifo raises the calling thread to SCHED_FIFO prio, bounded at ms.
// 0 when it holds the priority. Otherwise the errno, with the thread as it
// was: *bound_err non-zero means the bound could not be set (so the
// priority was not asked for); zero means the priority itself was refused
// (EPERM without CAP_SYS_NICE).
static int rtg_take_fifo(int prio, uint64_t ms, int *bound_err) {
    int e = rtg_arm(ms);
    if (bound_err) *bound_err = e;
    if (e != 0) return e;
    rtg_tid = (pid_t)syscall(SYS_gettid);
    struct sched_param sp = { .sched_priority = prio };
    if (sched_setscheduler(0, SCHED_FIFO, &sp) != 0) {
        e = errno;
        rtg_tid = 0;
        return e;
    }
    return 0;
}

#endif
