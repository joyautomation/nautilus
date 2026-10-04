---
title: Scan timing and real-time
description: What the scan diagnostics measure, how to read lateness, and how to make a fast task start on time — absolute scheduling, task isolation, priority, and what the kernel still owns.
---

Nautilus runs on a general-purpose operating system, so it is **soft
real-time**: a task is scheduled to start on a fixed period, and the
runtime measures and shows how well that promise is kept. This guide is
what the numbers mean, what the runtime does to keep them small, and what
you can do on the machine. Every figure quoted here comes from a written-up
measurement in `docs/design/realtime.md` in the repository, with the
hardware and method beside it; read that before quoting one.

Nothing here makes Nautilus a safety controller.

## What the diagnostics measure

The dashboard's **Scan diagnostics** panel (and `ScanDiagnostics` in the
HMI kit, and the `scan` block on `/api/state`) shows two different things:

- **Scan time** — how long a scan takes: read inputs, execute, write
  outputs. `lastMs`, `minMs`/`maxMs`, the 2 ms histogram, the phase bar.
  Usually dominated by I/O on the wire, not logic.
- **Lateness** — how late a scan *started* against its slot. Each task is
  scheduled on an absolute timeline (slot *n* is due at start + *n* ×
  period), and lateness is the time from the slot to the scan actually
  starting. This is the soft-real-time number, and it is kept per task,
  cumulative since the runtime started:

| field | meaning |
|---|---|
| `late` | scans that started more than `thresholdMs` after their slot |
| `thresholdMs` | the late threshold: `late-threshold:` on the task, default a tenth of the period |
| `overruns` | scans whose execution took longer than the period |
| `missed` | slots the loop skipped because it had fallen more than a whole period behind (after an overrun or a stall) — scans that should have run and did not |
| `p50Us`, `p99Us`, `p999Us`, `maxUs` | percentiles and worst case over every scan so far; percentiles are bucket upper edges, at most 3 % above the true value |
| `histogram`, `bucketsUs` | a 1-2-5 log-spaced histogram from 1 µs to 100 ms |

A task that is never late and never misses is doing what it promised. A
healthy 100 ms task on an ordinary Linux box is typically tens of
microseconds late at p99; a 1 ms task is where the rest of this page
starts to matter.

```yaml
tasks:
  - program: fast.st
    scan: 1ms
    late-threshold: 50us   # count it late beyond 50 µs (default: 100 µs, a tenth)
```

## What the runtime does

Three things, all automatic:

**Absolute schedule.** A late wake-up shortens the next sleep instead of
pushing it later, so lateness never accumulates; a loop more than a whole
period behind skips the lost slots and reports them as `missed` rather
than firing a burst of catch-up scans. On Linux the sleep is the kernel's
own absolute sleep (`clock_nanosleep`) on a thread of the task's own, with
the thread's timer slack reduced — a plain Go timer rounds any sleep under
a millisecond *up* to a millisecond, which is why this matters for fast
tasks.

**Task isolation.** Tasks run concurrently. Each scan copies its program's
external tags in, executes privately, and commits what changed as one
unit, so a fast task never waits behind a slow one and a reader never sees
a scan half-done. The one rule that follows: two tasks writing the same tag
resolve by commit order (last wins), as on any PLC with shared globals —
give a tag one owner.

**An allocation-free scan path.** The VM makes no allocation per builtin
call, user-function call, or function-block step, so the scan loop itself
never gives the garbage collector work. What the rest of the process does
(the tag API, MQTT, a historian) still can; see the collector note below.

Measured on a busy desktop with a stock kernel: a 1 ms task alone runs
every scan with p99 lateness around 30–55 µs; a 20 ms task beside it
changes nothing it can see.

## What you can do on the machine

### Priority and pinning

```yaml
tasks:
  - program: fast.st
    scan: 1ms
    priority: 50   # SCHED_FIFO at this priority (Linux, 1–99)
    cpu: 2         # pin the task's thread to this core (Linux)
```

**`priority:` is the setting that pays.** Under `SCHED_FIFO` the task
preempts ordinary load instead of queueing behind it: on the same busy
desktop, p99 went from 55 µs to 20 µs and the worst case from 2.3 ms to
0.3 ms. It needs `CAP_SYS_NICE`:

```sh
sudo setcap cap_sys_nice+ep ./my-controller     # or run as root, or set an rtprio rlimit
```

**A refused request is loud.** The runtime logs an error with the fix, the
task's row on the dashboard shows `cpu 2 · fifo 50 REFUSED`, and the task
runs at normal priority — it never pretends.

**`cpu:` alone makes things worse.** Pinned, a task can no longer move off
a core something else just landed on, and at normal priority it has no
claim to go first: measured, p99 went from 55 µs to 300–400 µs. Set `cpu:`
only together with `priority:`, or on a core the kernel keeps empty.

Mind the kernel's real-time throttle: by default `SCHED_FIFO` tasks get
95 % of every second (`/proc/sys/kernel/sched_rt_runtime_us`). A task that
overruns its period continuously is throttled, and that shows as missed
slots.

### Keeping a core quiet

For a pinned core to be empty rather than merely preferred, the kernel has
to be told at boot (`/etc/default/grub`, then reboot):

- `isolcpus=managed_irq,domain,2-3` keeps the scheduler's load balancer off
  cores 2–3; only threads pinned there run there.
- `nohz_full=2-3` stops the periodic scheduler tick on those cores while a
  single thread runs; `rcu_nocbs=2-3` moves RCU callbacks off them.
- `irqaffinity=0-1` keeps interrupt handling off them. The fieldbus NIC's
  interrupt is the one worth keeping *near* the task's core instead.
- On hybrid CPUs, pin to a performance core and isolate its SMT sibling too.

A `PREEMPT_RT` kernel lowers the floor further by making the kernel itself
preemptible; the measurements for that are an open item in the design doc.

### The garbage collector

The scan path allocates nothing, so the collector runs for whatever else
the process does. Its stop-the-world pauses are short (tens to hundreds of
microseconds; up to a few milliseconds under heavy allocation elsewhere)
and stop every thread, pinned or not. On a controller with memory to
spare, let the heap grow and collect rarely:

```sh
GOGC=off GOMEMLIMIT=256MiB ./my-controller
```

Measured with 50 MB/s of allocation from elsewhere in the process: the
default setting collected 448 times a minute with a worst pause of 2.1 ms;
with a memory limit and `GOGC=off`, 12 times with a worst pause of 82 µs.
The fast task's p99 did not change either way; the worst pause did.

## Measuring it yourself

`go run ./tools/jitter -duration 5m -out report` in the repository runs a
mixed resource (a 1 ms task on loopback I/O, a 10 ms task, a deliberately
slow task, an allocation-heavy task) on the real scheduler and writes a
report with the machine, kernel, Go version and settings beside the
numbers. Run it before and after a change on the same box and the two
reports are your before/after. `-cpu` and `-priority` place the fast task.

## Harder real-time

A loop that must be tens of microseconds, cycle-exact, or must keep running
through a Linux reboot is not a Nautilus task: it is a separate fast loop
beside Nautilus — a Rust process on an isolated core, or a microcontroller
owning the pins — exchanging a small set of tags with the supervisor. The
repository's `rt/` directory holds the spikes and `docs/design/realtime.md`
the measurements and the recommendation (fixed fast blocks configured from
the manifest, not a second logic engine). That work is in progress.
