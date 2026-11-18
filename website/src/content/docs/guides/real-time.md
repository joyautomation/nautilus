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
| `p50Us`, `p99Us`, `p999Us`, `maxUs` | percentiles and worst case over every scan so far; percentiles are bucket upper edges, about 3 % above the true value (the bucket width is 1/32, 3.125 %) |
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
a scan half-done. The commit lands only the members, and the bits of a
word, the program assigned, so a write to another member of the same
struct tag, or another bit of the same word, survives it. The one rule
that follows: two tasks writing the same tag, member or bit resolve by
commit order (last wins), as on any PLC with shared globals — give a tag
one owner.
The commit allocates nothing in the common case, when nothing else wrote
the tag during the scan. When something did (a client's write, another
task, a force on the tag), the member-by-member merge copies the
containers along the assigned paths, so that scan allocates a little more.

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

**Not beside a vendor's real-time thread.** Before a task is pinned to a
CPU at a priority, the runtime looks at what already runs real-time there.
It refuses when another program has a real-time thread on that CPU at a
**higher** priority. That is a vendor runtime or an I/O shim, something
the box depends on, and putting real-time work beside it starves whatever
else that core carries. On one controller that took the network down until
a power cycle. The refusal is loud: an error in the log, a `REAL-TIME
REFUSED` note on the startup banner, and the reason in the task's
`scan.sched`. The task runs unpinned at normal priority. Pin it to another
CPU, or set `share-cpu: true` on the task if you mean it. A real-time
thread at the same or a lower priority there is a warning, and so is the
kernel's real-time throttle being off (`sched_rt_runtime_us` -1 on any
kernel; the whole period only before 6.12, after which the fair server
makes that the normal setting).
Kernel threads don't count: every core has some at the top priority. A
higher thread that may run on other CPUs too (a desktop's `rtkit-daemon`,
for one) is only a warning. The check is what the refusal describes: a
thread pinned where yours is going.

The check runs once, as each task starts, and it is best effort. It sees
what `/proc` shows this process. When `/proc` is mounted with `hidepid`,
or entries can't be read, it says so in a warning instead of passing them
as safe. Inside a container's PID namespace it sees only that namespace.
A real-time thread started after nautilus is not checked.

**Not above the network's own threads.** Kernel threads otherwise don't
count, with one exception: the kernel's network threads. A NIC's IRQ
thread (`irq/31-eth0`) or its NAPI receive thread may be confined to the
CPU a task is pinned to, at a lower priority than the task. Then the start-up log warns, naming each one:
while the task runs, that CPU's network work waits. On a PLCnext the NIC's
NAPI receive thread runs at FIFO 1 on the same core as the I/O shim. A
FIFO 40 test task pinned there kept the box off the network for hours.
Pin real-time tasks to a CPU that carries no network work. A pinned FIFO
task also always outranks that CPU's `ksoftirqd/N`, which runs softirq
work the interrupts could not finish. There is one on every CPU, so it
gets no warning. Keep scans short, so the task sleeps most of each period
and the softirq work gets its turn.

**A thread that stops yielding is bounded.** Before a task gets
`SCHED_FIFO` the runtime sets `RLIMIT_RTTIME` on the process: a real-time
thread may run 200 ms without blocking (sleeping, waiting on I/O). A scan
takes far less, and the task sleeps between scans. A thread that passes
the limit gets `SIGXCPU`, and the runtime then moves every task with
`priority:` back to normal scheduling. It logs an error, and each task's
`scan.sched` shows `applied: false` with the reason, until the controller
restarts. Past 2.5 × the limit the kernel kills the process, in case that
did not happen. A task's `priority-guard: rt-limit:` tightens the limit,
from 10 ms up to the 200 ms default; it never loosens it, and a larger
value is refused. `NAUTILUS_RT_LIMIT_MS` (10 ms or more) on the service
replaces the default instead, so it can loosen it as well as tighten it.
The kernel keeps one limit for the whole process, so the strictest of
every task's `rt-limit`, the variable (or the default), and a limit
already in place (from a launcher, for instance) is the one in force;
each task's `guard` in `scan.sched` shows it as `rtLimitMs`.
A task that cannot be bounded is not given the priority.

What this limit does **not** catch:

- **An endless loop in your program.** That is the likeliest runaway, and
  this limit does not stop it. Go preempts the task about every 10 ms and
  parks its thread for a few microseconds, which resets the count, while
  the core stays all but full. The runtime's priority watchdog, below,
  catches that case.
- **A core with `nohz_full`.** The count advances once per scheduler tick,
  and the kernel stops the tick on a core whose only runnable thread is
  this one. There the 200 ms limit can take tens of seconds to trip.
- **A process with no spare Go processor.** The demotion runs on a Go
  goroutine. With `GOMAXPROCS=1`, or while the garbage collector's
  stop-the-world waits on a thread that cannot be preempted, nothing runs
  it, and the outcome is the hard limit's kill instead. Under
  `naut deploy` the service restarts, and on a PLCnext the shim's output
  watchdog drives the outputs safe meanwhile.

**The runtime watches its own priority tasks too.** A task at `priority:`
loses the priority the same way in two cases. One is overrunning its
period 50 times in a row. The other is a single scan running for more than
20 periods, and never less than 50 ms, so that one stall of a few tens of
milliseconds is not mistaken for a runaway. The second check runs while
the scan is still going, so it catches a scan that never ends, which the
kernel limit misses when the scan keeps getting preempted. Only that task
is demoted, and it stays demoted until a restart. Its scan carries on at
normal priority: it still uses CPU and the main task still reads, runs
and writes. It now takes its turn with everything else, so expect more
lateness. To see a demotion:

- the dashboard's task row shows `fifo 40 DEMOTED (overrun)`;
- the task's `sched` in `GET /api/state` (`scan.sched` for the main
  task, `scan.tasks[].sched` for the others) shows `demoted: true` and the
  reason, beside the `guard` it ran under;
- the log has an error;
- a `type: runtime` status driver, if the project has one, sets a tag an
  alarm can watch (below).

**Alarm on the runtime's own health.** A `type: runtime` driver delivers
a task's health as ordinary input tags, so an alarm, the historian or
Sparkplug sees it:

```yaml
drivers:
  - name: plc
    type: plcnext
    manifest: plcnext_manifest.yaml
  - name: health
    type: runtime
    scan-rate: 1s            # how often it reads the runtime's stats (default 1s)
    tags:
      - {name: Main__Demoted,     task: main, status: demoted}      # BOOL: priority withdrawn, until restart
      - {name: Main__Faulting,    task: main, status: faulting}     # BOOL: the last scan raised a logic error
      - {name: Main__LogicErrors, task: main, status: logic-errors} # UDINT, since start
      - {name: Fast__Late,        task: fast, status: late}         # UDINT: scans that woke late
      - {name: Fast__Overruns,    task: fast, status: overruns}     # UDINT: scans longer than the period
```

Declare each tag `role: input` with that type. `task:` is `main` or a
task's name (an unnamed task's is its program's file name without the
extension); `naut check` refuses one the project does not have. Every
status reads false / 0 until the first poll, so a scan never waits on it.
Alone, as the project's only `driver:`, it keeps the memory loopback a
project with no driver has, so a simulation can use it too. `tags:` on
any other driver type is refused.

Things to know:

- **Sparkplug.** In a `drivers:` list it is a Sparkplug device like any
  other driver (named by its `name:`), its statuses its metrics. It has
  no link of its own to lose, so it is born with the node and never sends
  DDEATH.
- **A failing driver beside it freezes it.** The runtime reads a
  `drivers:` list as one read, and a driver whose read returns an error
  fails the whole read: the runtime keeps last-known values, these
  included, until that driver reads again. Most drivers report a lost link
  as quality and keep reading, so this is the exception, but while it
  lasts these tags do not move.
- **Redundancy.** A standby does not scan, so these tags move only on the
  controller that leads: each reports its own runtime while it leads. A
  standby's own health is in its dashboard and `/api/stats`, not in tags.
  As with every input on a standby, what it serves (the OPC UA server, the
  live stream) is the last value it read, still marked Good.

Loosen the bounds per task where a task legitimately runs long now and
then:

```yaml
tasks:
  - program: fast.st
    scan: 1ms
    priority: 40
    priority-guard:
      overruns: 200       # scans in a row over the period (default 50, at most 100000)
      scan-multiple: 100  # one scan longer than this many periods (default 20, from 2 to 10000)
      rt-limit: 50ms      # tighten the kernel bound above (from 10ms to the 200ms default; the strictest task wins)
```

**`cpu:` alone makes things worse.** Pinned, a task can no longer move off
a core something else just landed on, and at normal priority it has no
claim to go first: measured, p99 went from 55 µs to 300–400 µs. Set `cpu:`
only together with `priority:`, or on a core the kernel keeps empty.

**On a box with few cores, the runtime raises `GOMAXPROCS`.** Go runs
code on `GOMAXPROCS` slots, which default to the number of cores. A task
waking at `SCHED_FIFO` still needs a free slot, and on a two-core box a
long scan in another task and the garbage collector can hold both, so the
task waits for Go's own preemption (around 10 ms), whatever its priority.
When a task has `priority:`, `GOMAXPROCS` is not set in the environment,
no CPU limit applies and the box has at most 4 cores, the runtime sets it
to the cores plus one for each task requesting a priority plus one, at
most twice the cores plus two.
That's 4 on a two-core box with one such task. On a two-core ARM module
this took a 1 ms task at FIFO 80, beside a task running 20 ms of logic,
from 95.8 % of its slots run and 9.22 ms late at p99.9 to 99.92 % and
188 µs (the [Verdin guide](/guides/verdin/#real-time-on-the-am62) has
the table). The banner says when it was raised, and `GET /api/state`
reports it as `scan.gomaxprocs`, with the reason when it was kept.

- **A CPU limit keeps the default.** Under a cgroup CPU quota (`docker run
  --cpus`, a systemd `CPUQuota=`), more Ps than the quota allows would be
  throttled, which means spikes. So the runtime leaves the value Go chose
  (it already follows the quota) and logs why.
- **A `GOMAXPROCS` in the environment always wins**, when it is a positive
  whole number, the same test Go applies. Setting it also turns off Go's
  own automatic updates of `GOMAXPROCS` when the cgroup limit changes, and
  so does the runtime's raise.

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

**On a controller, carefully.** Real-time load on a controller's cores
can take the box off the network. On a PLCnext, a 1 ms task at FIFO 40 on
the core of the vendor's I/O shim and the NIC's receive thread kept it
unreachable for about 2.4 hours. Before such a run:

- Pin it to a core with no vendor runtime, shim or network thread.
  nautilus's pre-flight refuses or warns at start, but choose the core
  yourself.
- Ask whoever owns the box.
- Do a 30-second run first, with `ping` to the box running alongside.

jitter guards its own run, too:

- **It stops a run the box can't keep up with.** That's 2 s without a scan
  of the fast task, a priority withdrawn mid-run, or the run going past
  1.5 × its duration plus 10 s. It writes an `INVALID` report and exits 3.
- **If its loops don't end when stopped, it exits.** Ending the process
  is what takes the load off the box.
- **A kernel timer SIGKILLs it 20 s after that,** whatever its threads
  are doing.

## Harder real-time

A loop that must be tens of microseconds, cycle-exact, or must keep running
through a Linux reboot is not a Nautilus task: it is a separate fast loop
beside Nautilus — a Rust process on an isolated core, or a microcontroller
— that **owns its own I/O** (a fieldbus, a serial line such as Modbus RTU
to fill heads, pins counting a flowmeter) and takes only parameters and
setpoints from Nautilus, handing results and faults back as tags. Nautilus
stays the line controller: sequencing, recipes, counts, alarms, the HMI.
The repository's `rt/` directory holds the spikes (timing and the tag
exchange only, no real I/O yet) and `docs/design/realtime.md` the
measurements and the recommendation: fixed fast blocks bound to that I/O
and configured from the manifest, not a second logic engine. That work is
in progress.
