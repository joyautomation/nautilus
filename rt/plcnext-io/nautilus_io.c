// nautilus_io — the in-process tier of the plcnext driver.
//
// A PLCnext "runtime" process in the sense of Phoenix Contact's
// SampleRuntime: started by the firmware's ACF alongside the plcnext
// process (see NautilusIo.acf.config), it joins the firmware as a module
// application and then touches the Axioline process image through the
// ANSI-C API — the GDS frame buffers "1:IN" and "1:OUT" of Arp.Io.AxlC —
// inside its own SCHED_FIFO cycle, exactly as the eCLR's tasks do. No
// socket, no serialisation, no connector: the image at the bus rate.
//
// It needs no PLCnext SDK. Every entry point it uses is a C export of a
// library the firmware already ships, so it is built with any ARMv7
// glibc toolchain and binds at run time:
//   /usr/lib/libArp.System.Module.so   ArpSystemModuleMain2(name, settings)
//                                       — what the SDK's static ModuleLib's
//                                       ArpSystemModule_Setup() wraps
//   /usr/lib/libArp.Plc.AnsiC.so       ArpPlcIo_* / ArpPlcGds_* / ArpPlcDomain_*
//                                       — loaded INTO this process by the
//                                       ACF (the Library/Component elements
//                                       of the acf.config); this code waits
//                                       for that and never loads it itself
//
// One rule above all others, learned on the bench: a declared ACF process
// that exits during the firmware's start-up phase takes the whole plcnext
// runtime down ("Could not start all processes"). So this process never
// exits on its own: every failure is logged and it idles.
//
// Stage 1 proved the path on the bench (join, acquire the frames, a
// heartbeat at the cycle rate). Stage 2 (this file) is the I/O side of the
// plcnext driver's `transport: shm`: every cycle it publishes the input
// frame, the bus RUN bit and its own timing into the exchange page
// (nautilus_io_shm.h, rt/fastloop's seqlock shape) and writes the output
// bytes nautilus left there into the output frame — or zeros, before
// nautilus has written and after its outputs go stale.
//
// Build: rt/plcnext-io/build.sh (zig cc for arm-linux-gnueabihf.2.35); a
// CLI release attaches the same build as nautilus-plcnext-io_<version>_linux_armv7.
// (nautilus_io_shm.h beside it is the exchange page's layout.)

#define _GNU_SOURCE
#include "nautilus_io_shm.h"
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <signal.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <syslog.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>
#include <linux/limits.h>

#ifndef NAUTILUS_IO_VERSION
#define NAUTILUS_IO_VERSION "dev" // build.sh stamps the release version
#endif

// ── the firmware's C surface, declared by hand ──────────────────────────
// Signatures follow Arp/Plc/AnsiC/{Io/FbIoSystem.h,Gds/DataLayout.h,
// Domain/PlcManager.h} as used by PLCnext/SampleRuntime; bool is C99 _Bool.

typedef struct TGdsBuffer TGdsBuffer;

enum PlcOperation {
    PlcOperation_None = 0, PlcOperation_Load, PlcOperation_Setup, PlcOperation_StartCold,
    PlcOperation_StartWarm, PlcOperation_StartHot, PlcOperation_Stop, PlcOperation_Reset,
    PlcOperation_Unload, PlcOperation_None2
};
typedef void (*PlcOperationHandler)(enum PlcOperation);

static void *(*p_ModuleMain2)(const char *appName, const char *settingsPath);
static bool (*p_GetBufferPtrByBufferID)(const char *ioSystem, const char *bufferId, TGdsBuffer **out);
static bool (*p_ReleaseGdsBuffer)(TGdsBuffer *b);
static bool (*p_GetVariableOffset)(TGdsBuffer *b, const char *name, size_t *offset);
static bool (*p_GetVariableBitOffset)(TGdsBuffer *b, const char *name, size_t *offset, unsigned char *bit);
static bool (*p_BeginRead)(TGdsBuffer *b, char **page);
static bool (*p_EndRead)(TGdsBuffer *b);
static bool (*p_BeginWrite)(TGdsBuffer *b, char **page);
static bool (*p_EndWrite)(TGdsBuffer *b);
static void (*p_SetHandler)(PlcOperationHandler h);
static const char *(*p_GetLastError)(void);

// ── logging: one plain file next to the binary (logs/nautilus_io.log);
// stdout/stderr are pointed at it too, so a C++ terminate() message from a
// firmware library lands somewhere a person can read ─────────────────────
static int logfd = -1;
static void logln(const char *fmt, ...) {
    char buf[600];
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    struct tm tm;
    localtime_r(&ts.tv_sec, &tm);
    int n = snprintf(buf, sizeof buf, "%02d:%02d:%02d.%03ld ", tm.tm_hour, tm.tm_min, tm.tm_sec, ts.tv_nsec / 1000000);
    va_list ap;
    va_start(ap, fmt);
    n += vsnprintf(buf + n, sizeof buf - (size_t)n - 1, fmt, ap);
    va_end(ap);
    if (n > (int)sizeof buf - 2) n = (int)sizeof buf - 2;
    buf[n++] = '\n';
    if (logfd >= 0) {
        ssize_t w = write(logfd, buf, (size_t)n);
        (void)w;
    } else {
        syslog(LOG_INFO, "%.*s", n - 1, buf);
    }
}

// idle never returns: the ACF owns this process's lifetime.
static void idle(const char *why) {
    logln("idling: %s", why);
    for (;;) pause();
}

// A crash is logged by signal number, then the default action is restored
// and the signal re-raised: the ACF sees an exit it can report, not a hang.
static void on_crash(int sig) {
    static const char msg[] = "FATAL signal in nautilus_io: ";
    char d[4] = {0};
    d[0] = (char)('0' + sig / 10); d[1] = (char)('0' + sig % 10); d[2] = '\n';
    if (logfd >= 0) { ssize_t w = write(logfd, msg, sizeof msg - 1); w = write(logfd, d, 3); (void)w; }
    signal(sig, SIG_DFL);
    raise(sig);
}

static void *need(void *lib, const char *sym) {
    void *p = dlsym(lib, sym);
    if (!p) {
        logln("missing symbol %s: %s", sym, dlerror());
        idle("a firmware symbol is missing");
    }
    return p;
}

// ── PLC state: the firmware tells us when the I/O domain is usable ──────
static volatile sig_atomic_t plc_state = PlcOperation_None;
static void on_plc(enum PlcOperation op) {
    plc_state = op;
    logln("plc operation %d", (int)op);
}

// ── the cycle ───────────────────────────────────────────────────────────
#define CYCLE_NS 1000000L   // 1 ms, a multiple of the 500 µs bus refresh
#define LATE_US 200         // a cycle this much behind its schedule counts as late
static volatile sig_atomic_t stopping;
static void on_term(int sig) { (void)sig; stopping = 1; }

static void ts_add(struct timespec *t, long ns) {
    t->tv_nsec += ns;
    while (t->tv_nsec >= 1000000000L) { t->tv_nsec -= 1000000000L; t->tv_sec++; }
}
static long ts_diff_us(const struct timespec *a, const struct timespec *b) {
    return (a->tv_sec - b->tv_sec) * 1000000L + (a->tv_nsec - b->tv_nsec) / 1000L;
}
static uint64_t ts_ns(const struct timespec *t) {
    return (uint64_t)t->tv_sec * 1000000000ULL + (uint64_t)t->tv_nsec;
}

// ── the exchange page with nautilus (nautilus_io_shm.h) ─────────────────
// Pure POSIX: nothing here touches the firmware, so a failure is logged
// and retried from the cycle, never fatal.
_Static_assert(NIO_OFF_ECHO + NIO_FRAME_LEN <= NIO_OFF_OUT_SEQ, "inputs block overlaps the outputs block");
_Static_assert(NIO_OFF_OUTPUTS + NIO_FRAME_LEN <= NIO_SHM_SIZE, "outputs block runs past the page");

static unsigned char *pg;
#define PG32(off) ((uint32_t *)(void *)(pg + (off)))
#define PG64(off) ((uint64_t *)(void *)(pg + (off)))

// page_open maps the page, creating it world read-write (umask
// notwithstanding) the way the firmware's own segments are, so naut as
// `admin` can map it too. A page left by an earlier run keeps nautilus's
// outputs block — nautilus may still be writing it — and gets a fresh
// header and inputs block.
static bool page_open(uint32_t period_ns, uint32_t late_us) {
    int fd = open(NIO_SHM_PATH, O_RDWR | O_CREAT | O_CLOEXEC, 0666);
    if (fd < 0) { logln("exchange page %s: %s", NIO_SHM_PATH, strerror(errno)); return false; }
    (void)fchmod(fd, 0666);
    struct stat st;
    if (fstat(fd, &st) != 0 || (st.st_size < NIO_SHM_SIZE && ftruncate(fd, NIO_SHM_SIZE) != 0)) {
        logln("exchange page size: %s", strerror(errno));
        close(fd);
        return false;
    }
    void *p = mmap(NULL, NIO_SHM_SIZE, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
    close(fd);
    if (p == MAP_FAILED) { logln("exchange page mmap: %s", strerror(errno)); return false; }
    // A page fault inside the cycle is a lateness spike; a refusal is harmless.
    (void)mlock(p, NIO_SHM_SIZE);
    pg = p;
    if (*PG64(NIO_OFF_MAGIC) != NIO_MAGIC || *PG32(NIO_OFF_VERSION) != NIO_VERSION)
        memset(pg, 0, NIO_SHM_SIZE);
    else
        memset(pg + NIO_OFF_STATUS, 0, NIO_OFF_OUT_SEQ - NIO_OFF_STATUS);
    // A writer that died mid-write left the sequence odd: make it even.
    uint32_t s = __atomic_load_n(PG32(NIO_OFF_IN_SEQ), __ATOMIC_RELAXED);
    __atomic_store_n(PG32(NIO_OFF_IN_SEQ), (s + 1) & ~1u, __ATOMIC_RELAXED);
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    *PG32(NIO_OFF_PERIOD_NS) = period_ns;
    *PG32(NIO_OFF_FRAME_LEN) = NIO_FRAME_LEN;
    *PG32(NIO_OFF_PID) = (uint32_t)getpid();
    *PG64(NIO_OFF_STARTED_NS) = ts_ns(&now);
    *PG32(NIO_OFF_LATE_US) = late_us;
    *PG32(NIO_OFF_VERSION) = NIO_VERSION;
    __atomic_store_n(PG64(NIO_OFF_MAGIC), NIO_MAGIC, __ATOMIC_RELEASE);  // last: the header is whole
    logln("exchange page %s mapped (%d bytes, period %u ns)", NIO_SHM_PATH, NIO_SHM_SIZE, period_ns);
    return true;
}

// Writer side of the inputs seqlock: odd, stores, even. Never waits.
static void in_begin(void) {
    uint32_t s = __atomic_load_n(PG32(NIO_OFF_IN_SEQ), __ATOMIC_RELAXED);
    __atomic_store_n(PG32(NIO_OFF_IN_SEQ), s + 1, __ATOMIC_RELAXED);
    __atomic_thread_fence(__ATOMIC_RELEASE);
}
static void in_end(void) {
    uint32_t s = __atomic_load_n(PG32(NIO_OFF_IN_SEQ), __ATOMIC_RELAXED);
    __atomic_store_n(PG32(NIO_OFF_IN_SEQ), s + 1, __ATOMIC_RELEASE);
}

// What nautilus asked for, as one consistent copy.
struct outs {
    uint32_t len, watchdog_ms;
    uint64_t ns, gen;
    unsigned char b[NIO_FRAME_LEN];
};

// Reader side of the outputs seqlock, bounded: false if nautilus was
// mid-write every time we looked (the caller keeps the last copy).
static bool out_read(struct outs *o) {
    struct outs t;
    for (int i = 0; i < 8; i++) {
        uint32_t s1 = __atomic_load_n(PG32(NIO_OFF_OUT_SEQ), __ATOMIC_ACQUIRE);
        if (s1 & 1) continue;
        t.len = *PG32(NIO_OFF_OUT_LEN);
        t.ns = *PG64(NIO_OFF_OUT_NS);
        t.gen = *PG64(NIO_OFF_OUT_GEN);
        t.watchdog_ms = *PG32(NIO_OFF_WATCHDOG_MS);
        memcpy(t.b, pg + NIO_OFF_OUTPUTS, NIO_FRAME_LEN);
        __atomic_thread_fence(__ATOMIC_ACQUIRE);
        if (__atomic_load_n(PG32(NIO_OFF_OUT_SEQ), __ATOMIC_RELAXED) == s1) {
            if (t.len > NIO_FRAME_LEN) t.len = NIO_FRAME_LEN;
            *o = t;
            return true;
        }
    }
    return false;
}

static void hex8(char *dst, const unsigned char *b) {
    snprintf(dst, 24, "%02x %02x %02x %02x %02x %02x %02x %02x", b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]);
}

int main(int argc, char **argv) {
    openlog("nautilus_io", LOG_CONS | LOG_PID | LOG_NDELAY, LOG_LOCAL1);
    // Resolve paths against the binary's directory: the ACF starts us with a
    // relative settings path and a working directory.
    char exe[PATH_MAX], settings[PATH_MAX], logpath[PATH_MAX];
    ssize_t n = readlink("/proc/self/exe", exe, sizeof exe - 1);
    if (n < 0) { syslog(LOG_ERR, "readlink: %s", strerror(errno)); idle("no exe path"); }
    exe[n] = 0;
    char *slash = strrchr(exe, '/');
    if (slash) *slash = 0;
    snprintf(logpath, sizeof logpath, "%s/logs/nautilus_io.log", exe);
    logfd = open(logpath, O_WRONLY | O_CREAT | O_APPEND, 0644);
    if (logfd >= 0) { dup2(logfd, 1); dup2(logfd, 2); }
    for (int s = 0; s < 4; s++) signal((int[]){SIGSEGV, SIGBUS, SIGABRT, SIGILL}[s], on_crash);
    signal(SIGTERM, on_term);
    signal(SIGINT, on_term);
    if (argc != 2) { logln("usage: nautilus_io <relative path to .acf.settings>"); idle("bad arguments"); }
    snprintf(settings, sizeof settings, "%s/%s", exe, argv[1]);
    logln("nautilus_io %s starting (pid %d, uid %d); settings %s", NAUTILUS_IO_VERSION, (int)getpid(), (int)getuid(), settings);

    // Join the firmware as a module application.
    void *mod = dlopen("/usr/lib/libArp.System.Module.so", RTLD_NOW | RTLD_GLOBAL);
    if (!mod) { logln("dlopen libArp.System.Module.so: %s", dlerror()); idle("no module library"); }
    p_ModuleMain2 = need(mod, "ArpSystemModuleMain2");
    void *dom = p_ModuleMain2("NautilusIo", settings);
    if (!dom) { logln("ArpSystemModuleMain2 returned null"); idle("the ACF did not accept this process"); }
    logln("joined the ACF as NautilusIo (app domain %p)", dom);

    // The ACF loads Arp.Plc.AnsiC.Library into this process itself (the
    // acf.config says so). Wait for that rather than racing it with a dlopen
    // of our own, which is the one way this process has crashed so far.
    void *ansic = NULL;
    for (int i = 0; !stopping && !ansic; i++) {
        ansic = dlopen("/usr/lib/libArp.Plc.AnsiC.so", RTLD_NOW | RTLD_NOLOAD);
        if (ansic) break;
        if (i % 20 == 0) logln("waiting for the ACF to load libArp.Plc.AnsiC.so into this process (%ds)", i / 2);
        usleep(500000);
    }
    if (!ansic) idle("stopped before the AnsiC library was loaded");
    logln("libArp.Plc.AnsiC.so is loaded in this process");
    p_GetBufferPtrByBufferID = need(ansic, "ArpPlcIo_GetBufferPtrByBufferID");
    p_ReleaseGdsBuffer = need(ansic, "ArpPlcIo_ReleaseGdsBuffer");
    p_GetVariableOffset = need(ansic, "ArpPlcGds_GetVariableOffset");
    p_GetVariableBitOffset = need(ansic, "ArpPlcGds_GetVariableBitOffset");
    p_BeginRead = need(ansic, "ArpPlcGds_BeginRead");
    p_EndRead = need(ansic, "ArpPlcGds_EndRead");
    p_BeginWrite = need(ansic, "ArpPlcGds_BeginWrite");
    p_EndWrite = need(ansic, "ArpPlcGds_EndWrite");
    p_SetHandler = need(ansic, "ArpPlcDomain_SetHandler");
    p_GetLastError = need(ansic, "ArpPlc_GetLastError");
    p_SetHandler(on_plc);
    logln("AnsiC entry points bound; PLC operation handler installed");

    // The library is loaded before the component behind it exists, and an
    // I/O call in that window throws an Arp::Exception through this C frame
    // (terminate, abort, and the whole runtime with it — seen on the
    // bench). The PLC operation callback is delivered by that component, so
    // its first Start* is the proof it is alive: nothing touches the ANSI-C
    // I/O surface before then.
    for (int i = 0; !stopping; i++) {
        int st = plc_state;
        if (st == PlcOperation_StartCold || st == PlcOperation_StartWarm || st == PlcOperation_StartHot) break;
        if (i % 20 == 0) logln("waiting for the PLC domain to start (operation %d so far, %ds)", st, i / 2);
        usleep(500000);
    }
    if (stopping) idle("stopped before the PLC domain started");
    logln("PLC domain started (operation %d); acquiring the Axioline frames", (int)plc_state);

    TGdsBuffer *in = NULL, *out = NULL, *diag = NULL;
    for (int tries = 0; !stopping && !(in && out); tries++) {
        if (!in) p_GetBufferPtrByBufferID("Arp.Io.AxlC", "1:IN", &in);
        if (!out) p_GetBufferPtrByBufferID("Arp.Io.AxlC", "1:OUT", &out);
        if (in && out) break;
        if (tries % 20 == 0)
            logln("waiting for Arp.Io.AxlC frames (plc state %d): %s", (int)plc_state, p_GetLastError());
        usleep(500000);
    }
    if (stopping) idle("stopped before the frames were acquired");
    // The frame length is what the TIC declared (1024 bytes, the DI4096 /
    // DO4096 ports 512 each); ArpPlcGds_GetLayoutMemorySize is not called
    // because its signature is not known for certain and a wrong guess
    // segfaulted on the bench. The offsets below are what matter.
    logln("frames acquired: 1:IN %p, 1:OUT %p", (void *)in, (void *)out);
    size_t di_off = 0, do_off = 0;
    bool have_di = p_GetVariableOffset(in, "Arp.Io.AxlC/0.DI4096", &di_off);
    bool have_do = p_GetVariableOffset(out, "Arp.Io.AxlC/0.DO4096", &do_off);
    logln("DI4096 %s offset %zu; DO4096 %s offset %zu", have_di ? "at" : "NOT FOUND,", di_off, have_do ? "at" : "NOT FOUND,", do_off);
    // DiagVars carries the bus's own "process data is being exchanged" bit,
    // the truth the gRPC path reads as AXIO_DIAG_STATUS_REG_RUN.
    size_t run_off = 0; unsigned char run_bit = 0; bool have_run = false;
    if (p_GetBufferPtrByBufferID("Arp.Io.AxlC", "DiagVars", &diag)) {
        if (p_GetVariableBitOffset(diag, "Arp.Io.AxlC/AXIO_DIAG_STATUS_REG_RUN", &run_off, &run_bit)) {
            logln("DiagVars acquired; RUN at %zu.%u", run_off, run_bit);
            have_run = true;
        }
    }
    if (!have_di || !have_do) idle("the DI4096/DO4096 ports are not in the frames; check the TIC");
    if (have_run) {
        // Once, logged on both sides, so a firmware that objects to a read of
        // this buffer says so at this line and not somewhere in the cycle.
        char *page;
        logln("reading DiagVars once before the cycle");
        if (p_BeginRead(diag, &page)) {
            unsigned char b[4];
            memcpy(b, page, sizeof b);
            int run = (((const unsigned char *)page)[run_off] >> run_bit) & 1;
            p_EndRead(diag);
            logln("DiagVars[0..4) %02x %02x %02x %02x; RUN = %d", b[0], b[1], b[2], b[3], run);
        } else {
            logln("DiagVars read refused (%s); RUN is not published", p_GetLastError());
            have_run = false;
        }
    }

    // Real-time cycle: pinned priority, absolute schedule, lateness kept.
    // The priority is 80 (over the firmware's interrupt threads at 50, a tie
    // with eth0's that made the cycle's lateness a coin toss under network
    // load; under T_Cyclic100 at 82, the network receive task at 95 and the
    // ESM at 97-98) unless nautilus_io.prio beside the binary says otherwise;
    // it only takes with CAP_SYS_NICE (setcap, README).
    int prio = 80;
    char priopath[PATH_MAX];
    snprintf(priopath, sizeof priopath, "%s/nautilus_io.prio", exe);
    FILE *pf = fopen(priopath, "r");
    if (pf) {
        int v;
        if (fscanf(pf, "%d", &v) == 1 && v >= 1 && v <= 96) prio = v;
        else logln("%s: want a priority 1..96; using %d", priopath, prio);
        fclose(pf);
    }
    bool fifo = false;
    struct sched_param sp = { .sched_priority = prio };
    if (sched_setscheduler(0, SCHED_FIFO, &sp) == 0) { fifo = true; logln("SCHED_FIFO priority %d", prio); }
    else logln("SCHED_FIFO %d refused (%s); running best-effort", prio, strerror(errno));
    // nautilus_io.cpu beside the binary pins the cycle to one CPU, away from
    // naut and from whichever core takes the network's receive work. Unlike
    // the priority this needs no capability: a thread may pin itself.
    int cpu = -1;
    snprintf(priopath, sizeof priopath, "%s/nautilus_io.cpu", exe);
    if ((pf = fopen(priopath, "r"))) {
        int v;
        if (fscanf(pf, "%d", &v) == 1 && v >= 0 && v < CPU_SETSIZE) cpu = v;
        else logln("%s: want a CPU number; not pinning", priopath);
        fclose(pf);
    }
    if (cpu >= 0) {
        cpu_set_t set;
        CPU_ZERO(&set);
        CPU_SET(cpu, &set);
        if (sched_setaffinity(0, sizeof set, &set) == 0) logln("cycle pinned to CPU %d", cpu);
        else { logln("pinning to CPU %d refused (%s); not pinned", cpu, strerror(errno)); cpu = -1; }
    }

    page_open((uint32_t)CYCLE_NS, LATE_US);

    struct timespec next, now;
    clock_gettime(CLOCK_MONOTONIC, &next);
    uint64_t cycles = 0, late = 0, frame_ns = 0, applied = 0, torn = 0;
    long worst_us = 0, worst_win = 0, worst_win_done = 0;
    static const long hist_le[] = NIO_HIST_LE_US;
    _Static_assert(sizeof hist_le / sizeof hist_le[0] == NIO_HIST_N - 1, "NIO_HIST_LE_US has NIO_HIST_N-1 bounds");
    uint64_t hist[NIO_HIST_N] = {0};
    struct outs o = {0};
    unsigned char inbuf[NIO_FRAME_LEN] = {0}, frame_out[NIO_FRAME_LEN] = {0};
    unsigned char last_in[8] = {0}, last_out[8] = {0};
    int changes = 0;          // change lines logged; capped, the 10 s line carries on
    bool was_run = false, was_wd = false, was_owned = false;
    char hi[24], ho[24];
    for (; !stopping; cycles++) {
        ts_add(&next, CYCLE_NS);
        clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, &next, NULL);
        clock_gettime(CLOCK_MONOTONIC, &now);
        long l = ts_diff_us(&now, &next);
        if (l < 0) l = 0;
        if (l > worst_us) worst_us = l;
        if (l > worst_win) worst_win = l;
        if (l > LATE_US) late++;
        int b = 0;
        while (b < NIO_HIST_N - 1 && l > hist_le[b]) b++;
        hist[b]++;
        uint64_t t0 = ts_ns(&now);
        if (!pg && cycles % 1000 == 999) page_open((uint32_t)CYCLE_NS, LATE_US);

        // A PLC that is not started (Stop, Reset, Unload in progress) may be
        // tearing the frames down; the cycle keeps time and stamps the page
        // but touches nothing of the firmware's.
        int st = plc_state;
        bool plc = st == PlcOperation_StartCold || st == PlcOperation_StartWarm || st == PlcOperation_StartHot;
        uint32_t status = NIO_ST_FRAMES | (fifo ? NIO_ST_FIFO : 0) | (plc ? NIO_ST_PLC : 0);
        if (plc) {
            char *page;
            if (p_BeginRead(in, &page)) {
                memcpy(inbuf, page + di_off, NIO_FRAME_LEN);
                p_EndRead(in);
                frame_ns = t0;
            }
            bool run = false;
            if (have_run && p_BeginRead(diag, &page)) {
                run = (((const unsigned char *)page)[run_off] >> run_bit) & 1;
                p_EndRead(diag);
                status |= NIO_ST_RUN_KNOWN | (run ? NIO_ST_RUN : 0);
            }
            if (run != was_run) { logln("bus RUN %d", run); was_run = run; }

            // The output frame, whole, every cycle: nautilus's bytes while
            // they are fresh, zeros before it has written and after its
            // watchdog lapses. Writing all of it means nothing depends on
            // what the firmware's write page held before.
            if (pg && !out_read(&o)) torn++;
            bool owned = o.len > 0;
            bool wd = owned && o.watchdog_ms && t0 > o.ns && t0 - o.ns > (uint64_t)o.watchdog_ms * 1000000ULL;
            if (owned && !wd) {
                memcpy(frame_out, o.b, o.len);
                memset(frame_out + o.len, 0, NIO_FRAME_LEN - o.len);
            } else {
                memset(frame_out, 0, NIO_FRAME_LEN);
            }
            if (wd) status |= NIO_ST_WATCHDOG;
            if (owned != was_owned) { logln("nautilus %s the outputs (%u bytes)", owned ? "owns" : "released", o.len); was_owned = owned; }
            if (wd != was_wd) {
                logln(wd ? "nautilus's outputs are older than %u ms: frame driven to zero" : "nautilus's outputs fresh again (watchdog %u ms)", o.watchdog_ms);
                was_wd = wd;
            }
            if (p_BeginWrite(out, &page)) {
                memcpy(page + do_off, frame_out, NIO_FRAME_LEN);
                p_EndWrite(out);
                if (owned && !wd) applied = o.gen;
            }

            bool ci = memcmp(inbuf, last_in, 8) != 0, co = memcmp(frame_out, last_out, 8) != 0;
            if ((ci || co) && changes < 64) {
                hex8(hi, inbuf); hex8(ho, frame_out);
                logln("in[0..8): %s  out[0..8): %s%s", hi, ho, ++changes == 64 ? "  (change lines stop here; the 10 s line carries on)" : "");
            }
            memcpy(last_in, inbuf, 8);
            memcpy(last_out, frame_out, 8);
        }

        if (pg) {
            in_begin();
            *PG32(NIO_OFF_STATUS) = status;
            *PG64(NIO_OFF_STAMP_NS) = t0;
            *PG64(NIO_OFF_FRAME_NS) = frame_ns;
            *PG64(NIO_OFF_CYCLES) = cycles + 1;
            *PG64(NIO_OFF_LATE) = late;
            *PG32(NIO_OFF_WORST_US) = (uint32_t)worst_us;
            *PG32(NIO_OFF_LAST_US) = (uint32_t)l;
            *PG64(NIO_OFF_APPLIED) = applied;
            *PG64(NIO_OFF_TORN) = torn;
            memcpy(pg + NIO_OFF_HIST, hist, sizeof hist);
            *PG32(NIO_OFF_WORST_WIN_US) = (uint32_t)worst_win_done;
            *PG32(NIO_OFF_PRIO) = (uint32_t)prio;
            *PG32(NIO_OFF_CPU) = (uint32_t)(cpu + 1);
            memcpy(pg + NIO_OFF_INPUTS, inbuf, NIO_FRAME_LEN);
            memcpy(pg + NIO_OFF_ECHO, frame_out, NIO_FRAME_LEN);
            in_end();
        }
        if (cycles % 10000 == 9999) {
            worst_win_done = worst_win;
            worst_win = 0;
            hex8(hi, inbuf); hex8(ho, frame_out);
            logln("%llu cycles, %llu late (>%d us), worst %ld us (%ld this window); in[0..8): %s  out[0..8): %s; gen %llu, torn %llu, status 0x%02x",
                  (unsigned long long)cycles + 1, (unsigned long long)late, LATE_US, worst_us, worst_win_done, hi, ho,
                  (unsigned long long)applied, (unsigned long long)torn, status);
        }
    }
    // Outputs are left as they are: the firmware's own fail-safe handling
    // decides what a stopped runtime's outputs do, not this process. The
    // page says the cycle has stopped.
    if (pg) { in_begin(); *PG32(NIO_OFF_STATUS) = 0; in_end(); }
    p_ReleaseGdsBuffer(in);
    p_ReleaseGdsBuffer(out);
    if (diag) p_ReleaseGdsBuffer(diag);
    logln("stopped after %llu cycles", (unsigned long long)cycles);
    idle("stopped; waiting for the ACF to end the process");
    return 0;
}
