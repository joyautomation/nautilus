# Real-time in Nautilus — design and measurements

Working document for making Nautilus's soft real-time measurably better and
for choosing a harder real-time path with evidence. Started 2026-10-03 from a
content-planning handoff; kept current on branch `rt-explore`.

**Status (2026-10-03):** Phase 1 (measure) is implemented with preliminary
numbers from one desktop (PR #113). Phase 2 item 1 (wake-up) is done on top
of it: an absolute schedule plus, on Linux, `clock_nanosleep` on a locked
thread with reduced timer slack — a 1 ms task's p99 start lateness went from
about a millisecond to 32 µs, and no ticks are dropped. Baselines on the
spare industrial PCs and an ARM board are pending (see "Open items").

## Why this exists

James is asking his audience, publicly, which way Nautilus should go for fast
logic (LinkedIn poll, Thu 2026-10-08): **a real-time kernel**, **pinned CPU
cores**, **a microcontroller doing the timing beside Linux** (e.g. the Arduino
UNO Q: a Debian-capable Qualcomm QRB2210 plus an STM32U585), or "don't need
it". The post places Nautilus in the same class as CODESYS Control on
Windows/Linux (soft real-time on a general-purpose OS; CODESYS's harder paths
are a PREEMPT_RT kernel on Linux or its RTE runtime on Windows). Pinned cores
came up on a customer call (don't name the company anywhere public).

Two jobs, in this order:

1. **Make the soft real-time we have measurably better, and provable.** This is
   worth doing whatever the poll says.
2. **Explore the hard(er) real-time paths** far enough to pick one with
   evidence: pinned Go, a Rust fast-loop process, a microcontroller, or a mix.

Everything public (posts, videos) will quote what this work measures, so: **no
claim without a measurement, on stated hardware, with the method written down.**

## What the runtime does today

- `runtime/runtime.go` `Run`: the main task and each extra task run in their
  own goroutine on a `time.NewTicker(scan)`; `Scan()` / `scanTask()` do
  read → execute → write (extra tasks: execute only; the main task owns driver I/O).
- `scanMu`: **one mutex serializes every task's scan.** A fast task waits
  behind a slow one — measured below, it is the single biggest effect.
- `ScanStats`: last/min/max/EWMA scan time; read/exec/write split; actual
  `PeriodMs`; `JitterMs` = EWMA of |period − target|; last 180 scan times and
  periods; `Histogram` = scan *execution* time in 2 ms buckets. **Phase 1 added
  `Lateness`** (below) to the main task and to every additional task.
- `runtime/clock.go`: the injectable `Clock`. Acceptance tests (`naut test`) run
  on **virtual time** and replay tick order without the tickers. Under a virtual
  clock every period is exactly the target, so the lateness tracker records
  zeros and the acceptance path is unchanged (`TestLatenessUnderVirtualClock`).
- Benchmarks: `runtime/scan_bench_test.go`, `hostdriver_bench_test.go`,
  `bigstore_bench_test.go`. On the desktop below: a full `Scan()` on the
  heated-tank program is 2.3 µs and **5 allocations (1 KB)**; the VM alone
  (`ProgramRun`) is 1.2 µs and 2 allocations.
- The core is **stdlib only** (HANDOFF.md). Linux syscalls go through `syscall`
  (or a justified, reviewed addition), behind build tags; `naut build`
  cross-compiles, so Windows/macOS must still build with no-ops.

## Phase 1 — measure (done, this branch)

### What was added

**`runtime.Lateness`**, on `ScanStats.Lateness` (main task) and
`TaskStats.Lateness` (each additional task), served on `/api/state` and the
SSE stream, shown on the built-in dashboard and the HMI's `ScanDiagnostics`:

| field | meaning |
|---|---|
| `thresholdMs` | lateness beyond which a scan counts as late. `Options.LateThreshold` / `Task.LateThreshold`; manifest `late-threshold:` per task (the first task's is the default for the rest). Unset = a tenth of the task's period. |
| `late` | scans whose period exceeded target + threshold |
| `overruns` | scans whose **execution** exceeded the period (the next slot was already due) |
| `missed` | slots the loop skipped after falling more than a whole period behind |
| `lastUs`, `maxUs` | the latest sample and the worst seen |
| `p50Us`, `p99Us`, `p999Us` | percentiles over every scan since start |
| `histogram`, `bucketsUs` | 1-2-5 log-spaced buckets, 1 µs … 100 ms, 17 buckets |

Definitions, which the harness shares:

- **A lateness sample is how late the scan started against its slot.**
  `Run` schedules slot n at start + n·period; the sample is scan start − slot
  time, never negative. It is taken before the scan lock, so waiting for
  another task shows up as scan time and overruns, not as lateness. When
  `Scan`/`ScanTask` are driven from outside `Run` (tests, a custom scheduler)
  there is no slot and the sample falls back to `period − target`, with an
  early scan landing in the first bucket. (Phase 1 as first merged used the
  period form everywhere; the slot form arrived with Phase 2 item 1, because
  once the schedule stops drifting the period form says nothing about phase.)
- **Missed** counts slots the loop skipped after falling a whole period
  behind: scans that should have run and did not. `ran / due` in a harness
  report is the same fact from the outside.
- Percentiles come from a log-linear histogram with 32 buckets per octave
  above 1 µs (about 3 % resolution) and are reported as the **upper edge** of
  the bucket the percentile falls in. A quoted p99 is a bound, never an
  interpolation below the truth.
- Everything is **cumulative since the runtime started**, cold start included.
  That is the number a report quotes ("over a 30-minute run …"). The 180-sample
  `periods` sparkline remains the live view.

Cost: one `record()` per scan under the lock the stats already take — a few
comparisons and two array increments. 8 KB of histogram per task.

**`tools/jitter`** — the repeatable harness (README there). It runs a 1 ms
main task with loopback I/O, a 10 ms task, a slow task calibrated to 20 ms of
VM time per scan, and an allocation-heavy task, on the real `runtime.Run`
tickers for a fixed duration, and writes `report.json` + `report.md` with the
machine, kernel, Go version, `GOMAXPROCS`, `GOGC`, governor and load average
written beside the numbers. Each task is switchable, so one box gives a
family of reports: fast tasks alone, fast + allocation, the full mix.

### Findings so far

All numbers below: **mira1**, Intel Core i9-14900K, Linux 6.16.3 (Pop!_OS,
stock kernel, `powersave` governor), Go 1.25.5, `GOMAXPROCS=32`, `GOGC=100`,
with the desktop **in use** (a QEMU VM and an ffmpeg encode at 25–40 % CPU
each, load average ≈ 6). Preliminary **60-second** runs; the 5-minute runs on
an idle box are an open item. Raw reports: `docs/design/realtime/2026-10-03-mira1-prelim/`.

**F1. `time.Ticker` loses 4–6 % of 1 ms ticks and drifts, by construction.**
A standalone test (no Nautilus code; scratch program, numbers in the table)
drove three wake-up strategies at a 1 ms period for 5 s:

| strategy | wakeups / expected | period error, mean | p50 | p99 | max | schedule drift at end |
|---|---:|---:|---:|---:|---:|---:|
| `time.Ticker` (what `Run` uses) | 4721 / 5000 | +59 µs | +67 µs | +140 µs | +1.0 ms | **+280 ms** |
| absolute deadline + `time.Sleep(until next)` | 5000 / 5000 | +0.1 µs | +65 µs | +139 µs | +1.3 ms | +0.3 ms |
| same, skipping missed slots | 4999 / 5000 | +0.2 µs | +64 µs | +145 µs | +1.3 ms | +0.9 ms |

The tick values the ticker *sends* are exactly 1.000 ms apart (Go stamps them
with the scheduled time), but each *receive* lands about one period plus the
wake-up overhead after the previous receive, so delivery slips ≈ 65 µs per
tick until a whole tick is skipped. The result is the same with and without
Go's network poller initialised (a live listener, as under `naut run`) and
with `GOMAXPROCS` 1, 2 or 32. The absolute-deadline loop holds the schedule
with the **same** per-wakeup latency. Measured against the *slot* rather
than the previous scan (which the period-error columns above cannot show),
the cause is a floor in Go's Linux runtime: an idle thread waits in
`epoll_wait` with a whole-millisecond timeout (`runtime/netpoll_epoll.go`
rounds any delay under 1 ms up to 1 ms), so **any Go sleep shorter than 1 ms
takes at least 1 ms.** A ticker turns that into drift; an absolute-deadline
Go timer turns it into a sawtooth of 0–1 ms lateness with catch-up scans.
The fix has to leave the Go timer path — see Phase 2 item 1 below.

**F2. The wake-up latency floor on this box is ≈ 70 µs typical, ≈ 200 µs p99**
(harness run A, fast tasks alone, main task at 1 ms): p50 70 µs, p99 204 µs,
p99.9 1.06 ms, max 3.04 ms; 12 % of scans more than 100 µs late; 57 487 of
60 000 scans ran (F1's loss). The default Linux timer slack for a normal
thread is 50 µs, which is most of the p50; `prctl(PR_SET_TIMERSLACK)`, a
locked thread, and (on PREEMPT_RT) `SCHED_FIFO` are the Phase 2 levers for
this number. The GC is not a factor here: 24 collections in 60 s, 1.7 ms of
stop-the-world in total, worst pause 98 µs.

**F3. One slow task wrecks every fast task — the lock, not the CPU.**
Harness run B (the full mix: the 20 ms slow task every 100 ms, the allocation
task every 50 ms): the 1 ms main task ran **35 559 of 60 000** scans, p99
lateness **27 ms**, max 178 ms, 605 overruns; the 10 ms task p99 53 ms. The
slow task itself was on time (p99 992 µs). Every fast scan that falls behind
the slow one waits for `scanMu`; with 32 idle cores. This is Phase 2 item 2,
and the handoff's instinct was right: the mutual exclusion that matters is
the tag store's write phase, not the whole scan.

**F4. The VM allocates inside loops, and that reaches the fast task through
the GC.** Run B saw **4 949 collections in 60 s** (270 ms of stop-the-world,
worst pause 5.2 ms) against 24 in run A. Run C (fast + allocation task, no
slow task) isolates the string-building task: 79 collections, worst pause
5.2 ms, and the main task's p99 goes 204 → 304 µs, max 3.0 → 4.4 ms. So the
big GC load in B comes from the slow task's plain `FOR … SQRT(INT_TO_REAL(i))`
loop, i.e. the VM's call path allocates per call (the handoff's "call
plumbing" suspicion). Phase 2 item 3 should profile `ProgramRun` on that
loop first; the 2 allocations per whole scan in the benchmark are not the
problem, the per-iteration ones are.

### Preliminary measurements (mira1, 60 s each)

Run A — fast tasks alone (`-slow 0 -alloc 0`):

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 57487 / 60000 | 7068 (12.29 %, thr 100 µs) | 3 | 70.0 µs | 204 µs | 1.06 ms | 3.04 ms |
| fast2 | 10 ms | 5999 / 6000 | 21 (0.35 %, thr 1 ms) | 0 | 1.0 µs | 944 µs | 1.09 ms | 3.87 ms |

GC: 24 collections, 1.7 ms total stop-the-world, pause p50 32.8 µs · p99 98.3 µs · max 98.3 µs.

Run B — full mix (defaults):

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 35559 / 60000 | 3998 (11.24 %, thr 100 µs) | 605 | 68.0 µs | 27.14 ms | 67.58 ms | 178.26 ms |
| fast2 | 10 ms | 4666 / 6000 | 611 (13.09 %, thr 1 ms) | 598 | 1.0 µs | 53.25 ms | 65.54 ms | 169.24 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 12.2 µs | 992 µs | 2.18 ms | 2.17 ms |
| alloc | 50 ms | 1197 / 1200 | 86 (7.18 %, thr 5 ms) | 126 | 1.0 µs | 22.02 ms | 26.11 ms | 129.26 ms |

GC: 4949 collections, 270.3 ms total stop-the-world, pause p50 20.5 µs · p99 115 µs · max 5.24 ms.

Run C — fast + allocation task (`-slow 0`):

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 57050 / 60001 | 9461 (16.58 %, thr 100 µs) | 9 | 74.0 µs | 304 µs | 976 µs | 4.41 ms |
| fast2 | 10 ms | 6000 / 6000 | 13 (0.22 %, thr 1 ms) | 0 | 1.0 µs | 944 µs | 1.06 ms | 1.08 ms |
| alloc | 50 ms | 1199 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 1.0 µs | 912 µs | 1.47 ms | 1.51 ms |

GC: 79 collections, 10.8 ms total stop-the-world, pause p50 32.8 µs · p99 164 µs · max 5.24 ms.

(The 10 ms task's p99 of ≈ 0.95 ms in every run is F1 seen from the other
side: when the 1 ms ticker skips, the 10 ms one, sharing the same mechanism,
occasionally lands a whole millisecond late.)

### Method (to reproduce)

```sh
go run ./tools/jitter -duration 5m -slow 0 -alloc 0 -out out/A -note "what the box was doing"
go run ./tools/jitter -duration 5m             -out out/B -note "…"
go run ./tools/jitter -duration 5m -slow 0     -out out/C -note "…"
```

Copy `report.md` into `docs/design/realtime/<date>-<host>-<label>/` and
quote from it. For a before/after, run the same three on the same box with
nothing else changed.

## Phase 2 — cheap soft-RT wins (days, measure each one separately)

Order revised by the findings; each is its own PR with a before/after from
the harness.

1. **Wake-up (F1, F2) — done, branch `rt-deadline-loop`.** `Run` now
   schedules every task on an absolute timeline (`runLoop`: slot n is due at
   start + n·period; a late wake shortens the next sleep instead of pushing
   it; more than a period behind, it skips the lost slots and reports them as
   `missed`). On Linux the sleep is `clock_nanosleep(CLOCK_MONOTONIC,
   TIMER_ABSTIME)` on the loop's own locked OS thread with the thread's timer
   slack set to 1 µs (`prctl`), chunked at 50 ms so a cancel is still prompt;
   elsewhere a Go timer (`runtime/sleep_linux.go`, `sleep_other.go`,
   `syscall` only). The virtual-time path does not use `Run`; the full
   acceptance suites pass.

   The standalone experiment behind it (`tools/jitter/wakeup`, 5 s at 1 ms,
   mira1 in use, slot lateness):

   | wake-up method | scans | p50 | p90 | p99 | p99.9 | max |
   |---|---:|---:|---:|---:|---:|---:|
   | Go timer, absolute deadline | 4999 | 535 µs | 952 µs | 1.05 ms | 1.32 ms | 1.87 ms |
   | `clock_nanosleep` absolute, locked thread | 4999 | 56 µs | 68 µs | 88 µs | 627 µs | 1.26 ms |
   | … and timer slack 1 ns | 4999 | 5 µs | 9 µs | 21 µs | 76 µs | 0.90 ms |

   The 50 µs step between the second and third rows is Linux's default
   timer slack for a normal thread; the millisecond in the first row is
   Go's poller. Neither needs a real-time kernel to remove.

   **Before/after on the harness (mira1 in use, 60 s, main task at 1 ms):**

   | run | before: scans ran | before: period error p50 / p99 | after: scans ran | after: slot lateness p50 / p99 / p99.9 / max | after: missed |
   |---|---:|---:|---:|---:|---:|
   | A fast tasks alone | 57 487 / 60 000 (95.8 %) | 70 µs / 204 µs | **60 003 / 60 005 (100 %)** | **6.2 µs / 31.5 µs / 344 µs / 2.53 ms** | 2 |
   | B full mix | 35 559 / 60 000 (59.3 %) | 68 µs / 27 ms | 51 418 / 60 011 (85.7 %) | 6.1 µs / 264 µs / 928 µs / 2.16 ms | 8 593 |
   | C fast + alloc | 57 050 / 60 001 (95.1 %) | 74 µs / 304 µs | **59 992 / 60 001 (100 %)** | 6.2 µs / 29.5 µs / 376 µs / 3.39 ms | 9 |

   The before and after columns are different measures (the ticker had no
   slot to measure against), which is why both are shown; "scans ran" is
   the comparable one. Run B's remaining loss is the scan lock (F3): the wait
   for it is counted as scan time, overruns (610) and missed slots, not as
   lateness, because lateness is taken at wake-up. The 10 ms task in run A:
   p50 8.8 µs, p99 54 µs, 6000 / 6000. Raw reports:
   `docs/design/realtime/2026-10-03-mira1-prelim-after-item1/`.

   Still open inside this item: the max column (2–3 ms, a handful of scans
   per minute) is the stock kernel's preemption latency under a busy
   desktop and is what item 4 (`SCHED_FIFO`, isolated cores, PREEMPT_RT) is
   for; and Windows/macOS keep the Go timer and its floor until someone
   measures them.
2. **Lock contention (F3):** per-task scan isolation instead of one `scanMu`.
   What actually needs mutual exclusion is the shared tag store's write phase;
   design it (snapshot in, commit out), don't just remove the lock. Measure a
   1 ms task beside a 20 ms one: the target is the slow task's presence not
   showing in the fast task's p99 at all.
3. **Allocation-free scan path (F4):** profile allocations in the VM on the
   `FOR … SQRT()` loop and the string task; drive the per-call allocations to
   0; then `GOGC`/`GOMEMLIMIT` guidance. Report GC pause count and max before/after.
4. **Pinning:** `runtime.LockOSThread` + `sched_setaffinity` per task, optional
   `SCHED_FIFO` (needs `CAP_SYS_NICE`; fail loudly, not silently), manifest
   `cpu:` / `priority:` per task, docs for `isolcpus` / `nohz_full` /
   `irqaffinity`. Measure on stock and PREEMPT_RT kernels. Estimate: pinning
   itself 1–2 days; a demonstrable before/after histogram about a week.

Honest ceiling to keep in the docs: Go's GC briefly stops every thread,
pinned or not (worst pause seen above: 5.2 ms, under an allocation-heavy
task), and on a stock kernel any thread can be preempted for milliseconds. This phase makes Nautilus **much better soft real-time**, not hard
real-time. Ethernet fieldbus jitter is separate and pinning doesn't fix it.

## Phase 3 — explore harder real-time (spikes, decide with evidence)

**A Rust fast-loop process** (James's idea): no GC; pinned to an isolated core
with RT priority on PREEMPT_RT; Nautilus stays the supervisor and exchanges a
small set of tags with it over **shared memory** (a seqlock or double buffer,
lock-free on the RT side). Spike: a 1 kHz loop in Rust, the shared-memory tag
exchange, and the same lateness report as Phase 1, side by side with pinned Go.

**A microcontroller** (STM32 class; the UNO Q's STM32U585 runs Zephyr): sub-µs,
keeps running if Linux reboots, owns pins (fast DI, encoders, PWM, ADC); link to
Nautilus over UART / SPI / RPMsg. Spike: the same fast loop, the same tag
exchange, measured on the pins (scope or logic analyzer if available).

| | Rust process, same Linux box | Microcontroller |
|---|---|---|
| Timing | µs to tens of µs (PREEMPT_RT, isolated core, tuned) | sub-µs, cycle-exact |
| Survives a Linux crash/update | no | yes (resilience, not a safety rating) |
| I/O | Ethernet fieldbuses (EtherCAT masters run like this), PCIe | pins |
| Compute | full | small |
| Deploy | one more binary | firmware, second toolchain, version pairing |
| Link | shared memory | UART / SPI / RPMsg |

Rule of thumb from the session: fast I/O on the network → Rust on real-time
Linux; fast I/O on pins → the microcontroller.

**The expensive question either way: what runs in the fast loop?** Nautilus
compiles IEC 61131-3 to its IR and runs it on a Go VM. Running user logic in the
fast loop means a Rust executor for the same IR with identical semantics,
cross-tested against the Go VM (the acceptance suites are the oracle). The
cheaper first step is a **fixed set of fast blocks** (PID, counters, a motion
profile) configured from the manifest. Recommend which, with the cost of each.

**One codebase, two targets** is the design to evaluate: a `no_std` Rust
executor built both as a pinned Linux process and as STM32 firmware (Embassy or
Zephyr), so the target is chosen per project by where the I/O lives.

## Constraints

- Virtual time (`naut test`) stays bit-identical. Run the full acceptance suites.
- Stdlib-only core; any new dependency is justified in the PR. Rust/firmware
  spikes live in their own directory (e.g. `rt/`) and don't touch the Go build.
- Cross-compile stays green (Windows/macOS no-op builds for Linux-only features).
- Safety is out of scope and says so: nothing here makes Nautilus a safety
  controller.
- CI only runs on pushes to main and on PRs: **open a draft PR early.**

## Deliverables

1. This file, kept current, with every measurement: hardware, kernel, Go/Rust
   versions, settings, raw report (`docs/design/realtime/`).
2. Phase 1 merged: late-scan counter, lateness histogram/percentiles, the harness. **← this branch**
3. Phase 2 changes, each its own PR with a before/after report.
4. Phase 3 spike reports and a recommendation (one paragraph, with the numbers).
5. A short "what we can now claim" list for James, with each claim's evidence.
   The follow-up post and a possible video are built from that list and nothing else.

## What we can claim now, and what we cannot

Claims below are supported by the preliminary desktop runs only; re-check
each against the 5-minute idle-box runs before quoting it.

- **"Nautilus measures its own scan lateness per task, to the microsecond,
  and shows it live"** — evidence: `runtime.Lateness`, the dashboard, this
  branch. Safe to say once merged.
- **"On a stock Linux desktop, with no kernel tuning, a 1 ms Nautilus task
  starts within 6 µs of its slot typically and within 32 µs at p99, and runs
  every scan"** — evidence: run A after item 1 (60 s, busy desktop; p99.9
  344 µs, max 2.5 ms, 2 missed of 60 005). Quote the max alongside, and say
  "stock kernel, desktop in use". Re-check on the 5-minute runs and on an
  industrial PC before publishing.
- **"Before this, a plain Go ticker dropped 4–6 % of 1 ms ticks and started
  scans anywhere in a 1 ms window; the fix is an absolute schedule and the
  kernel's own absolute sleep, not a real-time kernel"** — evidence: F1 and
  the wake-up table. This is the first before/after story (N-72).
- **Do not claim** fast tasks are isolated from slow ones: they are not (F3).
  That is the second before/after story, and the better video.

## Open items

- **Baselines not yet run** (James, 2026-10-03): the office cluster nodes
  are out — they are in service. No idle desktop yet. James has spare Linux
  boxes (a Tensor PC and VP6650s) that can be hooked up in a few days: those
  are the stock-kernel industrial-PC baseline, and candidates for a
  PREEMPT_RT kernel since they are not shared. ARM waits for an ARM Linux
  board. A Mac mini (Apple silicon) is available: useful as a quick arm64
  check of F1 (Go's ticker behaviour), not as a deployment baseline.
- The PREEMPT_RT comparison needs a kernel on a machine James nominates
  (the spare boxes are candidates; nobody else runs on them).
- A `jitter.yml` workflow runs the harness on a hosted runner nightly and on
  demand (relative numbers only, plus a ≥ 99 % scans-ran guard on the fast
  task) and uploads the reports; add the self-hosted boxes' labels to its
  matrix once they are runners — that is the baseline rig.
- `hmi` package: `types.ts` grew a `Lateness` interface and `ScanDiagnostics`
  shows it; a patch bump will be wanted when it ships (publish-on-bump).

## How to work

- Worktree `../nautilus-rt`, branch `rt-explore`, off `origin/main`.
- Ask James before buying hardware (a UNO Q, a logic analyzer) or installing an
  RT kernel on a shared machine, or running load on an in-service node.
- Report back in plain terms: what you measured, what changed, what it means.
