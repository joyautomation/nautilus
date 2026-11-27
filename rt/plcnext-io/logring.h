// logring.h: keep log I/O out of the real-time cycle (#1051).
//
// logln() used to write() to the log file (and syslog() without one) from
// the SCHED_FIFO cycle. A buffered write() blocks in dirty-page throttling
// or on filesystem locks when something else is flooding the flash (a 47 MB
// deploy copy stalled the cycle ~675 ms on the bench), and syslog() blocks
// on /dev/log. Now the producer side formats into a fixed slot of a
// bounded lock-free ring and returns: no syscall, no lock, no allocation,
// never waits. A SCHED_OTHER thread drains the ring to the file. A full
// ring drops the record and counts it; the drain thread reports the count.
//
// Multi-producer (the cycle, and the firmware's thread calling the PLC
// operation handler), single consumer: Vyukov's bounded queue, with the
// producer's retry loop bounded (a lost race retries a few times, then the
// record is dropped rather than spinning).
//
// Header-only, so nautilus_io.c and its test harness (ctest/) share one copy.
#ifndef LOGRING_H
#define LOGRING_H

#include <pthread.h>
#include <sched.h>
#include <stdio.h>
#include <stdatomic.h>
#include <stdint.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define LR_SLOTS 256 // power of two
#define LR_MSG 600

struct lr_slot {
    _Atomic uint32_t seq;
    uint16_t len;
    char msg[LR_MSG];
};

static struct lr_slot lr_ring[LR_SLOTS];
static _Atomic uint32_t lr_head;           // next slot to claim (producers)
static uint32_t lr_tail;                   // next slot to read (the drain thread only)
static _Atomic uint64_t lr_dropped;        // records refused because the ring was full
static _Atomic int lr_started;
static int lr_fd = -1;                     // where the drain thread writes
static void (*lr_fallback)(const char *, size_t); // no fd: e.g. syslog; drain thread only

static void lr_init(int fd, void (*fallback)(const char *, size_t)) {
    for (uint32_t i = 0; i < LR_SLOTS; i++) atomic_store(&lr_ring[i].seq, i);
    lr_fd = fd;
    lr_fallback = fallback;
}

// lr_push never blocks and makes no system call. false: dropped.
static int lr_push(const char *msg, size_t len) {
    if (len > LR_MSG) len = LR_MSG;
    for (int tries = 0; tries < 8; tries++) {
        uint32_t pos = atomic_load_explicit(&lr_head, memory_order_relaxed);
        struct lr_slot *s = &lr_ring[pos & (LR_SLOTS - 1)];
        uint32_t seq = atomic_load_explicit(&s->seq, memory_order_acquire);
        int32_t dif = (int32_t)(seq - pos);
        if (dif == 0) {
            if (atomic_compare_exchange_weak_explicit(&lr_head, &pos, pos + 1, memory_order_relaxed, memory_order_relaxed)) {
                memcpy(s->msg, msg, len);
                s->len = (uint16_t)len;
                atomic_store_explicit(&s->seq, pos + 1, memory_order_release);
                return 1;
            }
        } else if (dif < 0) {
            break; // full
        }
    }
    atomic_fetch_add_explicit(&lr_dropped, 1, memory_order_relaxed);
    return 0;
}

static void lr_emit(const char *m, size_t n) {
    if (lr_fd >= 0) {
        ssize_t w = write(lr_fd, m, n);
        (void)w;
    } else if (lr_fallback) {
        lr_fallback(m, n);
    }
}

// lr_drain_once moves what is queued to the sink; the count of records.
static int lr_drain_once(void) {
    int n = 0;
    for (;;) {
        struct lr_slot *s = &lr_ring[lr_tail & (LR_SLOTS - 1)];
        if (atomic_load_explicit(&s->seq, memory_order_acquire) != lr_tail + 1) break;
        lr_emit(s->msg, s->len);
        atomic_store_explicit(&s->seq, lr_tail + LR_SLOTS, memory_order_release);
        lr_tail++;
        n++;
    }
    return n;
}

static uint64_t lr_reported;
static void *lr_thread(void *arg) {
    (void)arg;
    for (;;) {
        if (!lr_drain_once()) {
            struct timespec ts = {0, 20 * 1000 * 1000};
            nanosleep(&ts, NULL);
        }
        uint64_t d = atomic_load(&lr_dropped);
        if (d != lr_reported) {
            char b[96];
            int n = snprintf(b, sizeof b, "log ring full: %llu records dropped so far\n", (unsigned long long)d);
            lr_reported = d;
            lr_emit(b, (size_t)n);
        }
    }
    return NULL;
}

// lr_start launches the drain thread at normal scheduling (it is started
// before the caller takes SCHED_FIFO, and a thread made later by a FIFO
// thread would inherit the policy, so it asks for SCHED_OTHER explicitly).
static int lr_start(void) {
    pthread_attr_t a;
    pthread_attr_init(&a);
    pthread_attr_setstacksize(&a, 64 * 1024);
    pthread_attr_setinheritsched(&a, PTHREAD_EXPLICIT_SCHED);
    struct sched_param sp = {0};
    pthread_attr_setschedpolicy(&a, SCHED_OTHER);
    pthread_attr_setschedparam(&a, &sp);
    pthread_t t;
    int e = pthread_create(&t, &a, lr_thread, NULL);
    pthread_attr_destroy(&a);
    if (e == 0) {
        pthread_detach(t);
        atomic_store(&lr_started, 1);
    }
    return e;
}

#endif
