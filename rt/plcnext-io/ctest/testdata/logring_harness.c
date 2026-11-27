// logring_harness: drives ../../logring.h for logring_test.go (#1051).
// The log sink is a pipe nobody reads, filled to the brim: any write() to
// it blocks forever, the way a write to a flash under writeback can for
// hundreds of ms. Modes:
//   ring    1500 one-millisecond cycles, each logging a long line through
//           the ring; the cycle must finish with lateness unchanged, and
//           drop records (the sink never drains)
//   deliver <file>  100 numbered lines through the ring to a plain file
//   direct  the same cycle, logging with a plain write() to the sink: the
//           control, which must hang
#define _GNU_SOURCE
#include "../../logring.h"
#include <fcntl.h>
#include <stdio.h>
#include <string.h>

int main(int argc, char **argv) {
    const char *mode = argc > 1 ? argv[1] : "";
    setvbuf(stdout, NULL, _IONBF, 0);
    if (strcmp(mode, "deliver") == 0 && argc > 2) {
        int fd = open(argv[2], O_WRONLY | O_CREAT | O_TRUNC, 0644);
        lr_init(fd, NULL);
        if (fd < 0 || lr_start() != 0) return 2;
        for (int i = 0; i < 100; i++) {
            char b[32];
            int n = snprintf(b, sizeof b, "line %d\n", i);
            while (!lr_push(b, (size_t)n)) usleep(1000); // the ring holds 256; never full here, but be exact
        }
        usleep(300000);
        return 0;
    }
    int p[2];
    if (pipe(p) != 0) return 2;
    fcntl(p[1], F_SETFL, O_NONBLOCK);
    char junk[4096];
    memset(junk, 'x', sizeof junk);
    while (write(p[1], junk, sizeof junk) > 0) {
    }
    fcntl(p[1], F_SETFL, 0); // blocking again, and full
    lr_init(p[1], NULL);
    int ring = strcmp(mode, "ring") == 0;
    if (ring && lr_start() != 0) return 2;

    char line[560];
    memset(line, 'l', sizeof line - 1);
    line[sizeof line - 1] = '\n';
    struct timespec next, now;
    clock_gettime(CLOCK_MONOTONIC, &next);
    long worst = 0;
    for (int i = 0; i < 1500; i++) {
        next.tv_nsec += 1000000;
        if (next.tv_nsec >= 1000000000L) { next.tv_nsec -= 1000000000L; next.tv_sec++; }
        clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, &next, NULL);
        clock_gettime(CLOCK_MONOTONIC, &now);
        long l = (now.tv_sec - next.tv_sec) * 1000000L + (now.tv_nsec - next.tv_nsec) / 1000L;
        if (l > worst) worst = l;
        if (ring) lr_push(line, sizeof line);
        else if (write(p[1], line, sizeof line) < 0) return 2;
    }
    printf("DONE worst_us=%ld dropped=%llu\n", worst, (unsigned long long)atomic_load(&lr_dropped));
    return 0;
}
