// rtguard_harness: drives ../../rtguard.h (the shim's real-time guard) for
// rtguard_test.go. Modes:
//   limit   arm at 50 ms and print this process's RLIMIT_RTTIME line
//   signal  arm, guard this thread, send SIGXCPU to itself: still alive?
//   spin    take SCHED_FIFO 1 bounded at 50 ms and spin until demoted
#define _GNU_SOURCE
#include "../../rtguard.h"
#include <stdio.h>
#include <string.h>
#include <time.h>

int main(int argc, char **argv) {
    const char *mode = argc > 1 ? argv[1] : "";
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
        int e = rtg_arm(50);
        if (e) { printf("ARM %s\n", strerror(e)); return 1; }
        rtg_tid = (pid_t)syscall(SYS_gettid);
        kill(getpid(), SIGXCPU);
        struct timespec ts = { 0, 50 * 1000 * 1000 };
        nanosleep(&ts, NULL);
        printf("alive demoted=%d policy=%d\n", (int)rtg_demoted, sched_getscheduler(0));
        return 0;
    }
    if (strcmp(mode, "spin") == 0) {
        int bound = 0;
        int e = rtg_take_fifo(1, 50, &bound);
        if (e) { printf("NOFIFO %s bound=%d\n", strerror(e), bound); return 0; }
        while (!rtg_demoted) {
        }
        printf("DEMOTED policy=%d\n", sched_getscheduler(0));
        return 0;
    }
    return 2;
}
