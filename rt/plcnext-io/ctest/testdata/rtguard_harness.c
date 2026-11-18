// rtguard_harness: drives ../../rtguard.h (the shim's real-time guard) for
// rtguard_test.go. Modes:
//   limit    arm at 50 ms and print this process's RLIMIT_RTTIME line
//   signal   take SCHED_FIFO 1 if allowed (else only arm), send SIGXCPU to
//            itself: still alive, and the thread at SCHED_OTHER?
//   spin     a second thread, with SIGXCPU blocked so the handler runs on
//            another thread, takes SCHED_FIFO 1 bounded at 50 ms and spins
//            until demoted: the handler must demote it by its tid
//   nobound  (built with -DRTG_TEST_FAIL_ARM) the bound cannot be set: the
//            priority must not be taken
#define _GNU_SOURCE
#include "../../rtguard.h"
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

static void *spinner(void *arg) {
    (void)arg;
    sigset_t xcpu;
    sigemptyset(&xcpu);
    sigaddset(&xcpu, SIGXCPU);
    pthread_sigmask(SIG_BLOCK, &xcpu, NULL);
    int bound = 0;
    int e = rtg_take_fifo(1, 50, &bound);
    if (e) {
        printf("NOFIFO %s bound=%d\n", strerror(e), bound);
        return NULL;
    }
    while (!rtg_demoted && !rtg_demote_errno) {
    }
    printf("DEMOTED policy=%d errno=%d\n", sched_getscheduler(0), (int)rtg_demote_errno);
    return NULL;
}

int main(int argc, char **argv) {
    const char *mode = argc > 1 ? argv[1] : "";
    setvbuf(stdout, NULL, _IONBF, 0);
    if (strcmp(mode, "limit") == 0) {
        int e = rtg_arm(50);
        if (e) { printf("ARM %s\n", strerror(e)); return 1; }
        FILE *f = fopen("/proc/self/limits", "r");
        char line[256];
        while (f && fgets(line, sizeof line, f))
            if (strncmp(line, "Max realtime timeout", 20) == 0) fputs(line, stdout);
        return 0;
    }
    if (strcmp(mode, "signal") == 0) {
        int bound = 0;
        int e = rtg_take_fifo(1, 50, &bound);
        if (bound) { printf("ARM %s\n", strerror(bound)); return 1; }
        if (e) rtg_tid = (pid_t)syscall(SYS_gettid); // no FIFO here: guard it anyway
        kill(getpid(), SIGXCPU);
        struct timespec ts = { 0, 50 * 1000 * 1000 };
        nanosleep(&ts, NULL);
        printf("alive fifo=%d demoted=%d policy=%d\n", e == 0, (int)rtg_demoted, sched_getscheduler(0));
        return 0;
    }
    if (strcmp(mode, "spin") == 0) {
        pthread_t t;
        pthread_create(&t, NULL, spinner, NULL);
        pthread_join(t, NULL);
        return 0;
    }
    if (strcmp(mode, "nobound") == 0) {
        int bound = 0;
        int e = rtg_take_fifo(1, 50, &bound);
        printf("taken=%d bound_err=%d policy=%d tid=%d\n", e == 0, bound, sched_getscheduler(0), (int)rtg_tid);
        return 0;
    }
    return 2;
}
