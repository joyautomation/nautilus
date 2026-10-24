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
// Stage 1 (this file): prove the path on the bench — join, acquire the
// frames, log the input bytes, run a heartbeat on one output bit at the
// cycle rate. Stage 2 adds the shared-memory exchange with nautilus
// (rt/fastloop's seqlock page) and the Go side's `transport: shm`.
//
// Build (from the repo root; zig is any recent release):
//   zig cc -target arm-linux-gnueabihf.2.35 -mcpu=cortex_a9 -O2 -std=c11 \
//          -o nautilus_io rt/plcnext-io/nautilus_io.c -ldl -lpthread

#define _GNU_SOURCE
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
#include <time.h>
#include <unistd.h>
#include <linux/limits.h>

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
static volatile sig_atomic_t stopping;
static void on_term(int sig) { (void)sig; stopping = 1; }

static void ts_add(struct timespec *t, long ns) {
    t->tv_nsec += ns;
    while (t->tv_nsec >= 1000000000L) { t->tv_nsec -= 1000000000L; t->tv_sec++; }
}
static long ts_diff_us(const struct timespec *a, const struct timespec *b) {
    return (a->tv_sec - b->tv_sec) * 1000000L + (a->tv_nsec - b->tv_nsec) / 1000L;
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
    logln("nautilus_io starting (pid %d, uid %d); settings %s", (int)getpid(), (int)getuid(), settings);

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
    if (p_GetBufferPtrByBufferID("Arp.Io.AxlC", "DiagVars", &diag)) {
        size_t off = 0; unsigned char bit = 0;
        if (p_GetVariableBitOffset(diag, "Arp.Io.AxlC/AXIO_DIAG_STATUS_REG_RUN", &off, &bit))
            logln("DiagVars acquired; RUN at %zu.%u", off, bit);
    }
    if (!have_di || !have_do) idle("the DI4096/DO4096 ports are not in the frames; check the TIC");

    // Real-time cycle: pinned priority, absolute schedule, lateness kept.
    struct sched_param sp = { .sched_priority = 50 };
    if (sched_setscheduler(0, SCHED_FIFO, &sp) != 0)
        logln("SCHED_FIFO refused (%s); running best-effort", strerror(errno));

    struct timespec next, now;
    clock_gettime(CLOCK_MONOTONIC, &next);
    uint64_t cycles = 0, late = 0; long worst_us = 0;
    unsigned char last_in[8] = {0};
    for (; !stopping; cycles++) {
        ts_add(&next, CYCLE_NS);
        clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, &next, NULL);
        clock_gettime(CLOCK_MONOTONIC, &now);
        long l = ts_diff_us(&now, &next);
        if (l > worst_us) worst_us = l;
        if (l > 200) late++;

        // A PLC that is not started (Stop, Reset, Unload in progress) may be
        // tearing the frames down; the cycle keeps time but touches nothing.
        int st = plc_state;
        if (st != PlcOperation_StartCold && st != PlcOperation_StartWarm && st != PlcOperation_StartHot) continue;

        char *page;
        unsigned char cur[8] = {0};
        if (p_BeginRead(in, &page)) {
            memcpy(cur, page + di_off, sizeof cur);
            p_EndRead(in);
        }
        if (memcmp(cur, last_in, sizeof cur) != 0) {
            logln("in[0..8): %02x %02x %02x %02x %02x %02x %02x %02x", cur[0], cur[1], cur[2], cur[3], cur[4], cur[5], cur[6], cur[7]);
            memcpy(last_in, cur, sizeof cur);
        }
        // Heartbeat on DO16 output 15: bit 15 of the big-endian word at
        // output bytes 2..3 (the module's frame in the generic layout).
        bool hb = (cycles / 500) % 2 == 0;   // 1 Hz at a 1 ms cycle
        if (p_BeginWrite(out, &page)) {
            unsigned char *w = (unsigned char *)page + do_off + 2;
            if (hb) w[0] |= 0x80; else w[0] &= (unsigned char)~0x80;
            p_EndWrite(out);
        }
        if (cycles % 10000 == 9999)
            logln("%llu cycles, %llu late (>200 us), worst %ld us", (unsigned long long)cycles, (unsigned long long)late, worst_us);
    }
    // Outputs are left as they are: the firmware's own fail-safe handling
    // decides what a stopped runtime's outputs do, not this process.
    p_ReleaseGdsBuffer(in);
    p_ReleaseGdsBuffer(out);
    if (diag) p_ReleaseGdsBuffer(diag);
    logln("stopped after %llu cycles", (unsigned long long)cycles);
    idle("stopped; waiting for the ACF to end the process");
    return 0;
}
