# Real-time in Nautilus — design and measurements

Working document for making Nautilus's soft real-time measurably better and
for choosing a harder real-time path with evidence. Started 2026-10-03 from a
content-planning handoff. Phase 1, Phase 2 (items 1–4) and the Phase 3 Rust
spike merged to `main` on 2026-10-04 (PRs #113, #115, #118, #122; #114, #116
and #120 were their stacked predecessors). The user-facing version of this is
the website guide `guides/real-time.md`.

**Status (2026-10-03):** Phase 1 (measure) is implemented with preliminary
numbers from one desktop (PR #113). Phase 2 item 1 (wake-up) is done on top
of it: an absolute schedule plus, on Linux, `clock_nanosleep` on a locked
thread with reduced timer slack — a 1 ms task's p99 start lateness went from
about a millisecond to 32 µs, and no ticks are dropped. Phase 2 item 2 (per-
task scan isolation) is done on top of that: a 20 ms task beside the 1 ms
task no longer delays it (p99 72 µs, 99.95 % of scans run, versus 59 % with
the old global scan lock). Phase 2 item 3 (allocation-free scan path) is done
on top of that: the VM makes no allocation per builtin call, user-function
call or FB step, so a loop-heavy task no longer drives the collector (3 889
collections a minute → 5; worst pause 2.1 ms → 0.2 ms). Phase 2 item 4
(pinning) is done: per-task `cpu:` / `priority:` with a loud, visible
refusal when the OS says no. Measured: affinity alone on a busy,
non-isolated desktop makes the fast task WORSE (p99 55 → 312–408 µs);
`SCHED_FIFO` is what pays — p99 55 → 20 µs, worst 2.3 ms → 0.3 ms, every
scan run, on the stock kernel. The isolated-core and PREEMPT_RT rows need a
dedicated box. Baselines on the spare industrial PCs and an ARM board are
pending; an Arduino UNO Q arrives for Phase 3 the week of 2026-10-06.

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
- Scan isolation: each `Program.Run` snapshots its VAR_EXTERNAL set in
  under one read lock, executes against the private copy, and commits what
  changed under one write lock (`runtime/scanview.go`, Phase 2 item 2).
  Before that, one mutex (`scanMu`) serialized every task's scan and a fast
  task waited behind a slow one — measured below as F3, it was the single
  biggest effect.
- `ScanStats`: last/min/max/EWMA scan time; read/exec/write split; actual
  `PeriodMs`; `JitterMs` = EWMA of |period − target|; last 180 scan times and
  periods; `Histogram` = scan *execution* time in 2 ms buckets. **Phase 1 added
  `Lateness`** (below) to the main task and to every additional task.
- `runtime/clock.go`: the injectable `Clock`. Acceptance tests (`naut test`) run
  on **virtual time** and replay tick order without the tickers. Under a virtual
  clock every period is exactly the target, so the lateness tracker records
  zeros and the acceptance path is unchanged (`TestLatenessUnderVirtualClock`).
- Benchmarks: `runtime/scan_bench_test.go`, `hostdriver_bench_test.go`,
  `bigstore_bench_test.go`, `loop_bench_test.go`. On the desktop below,
  after item 3: a full `Scan()` on the heated-tank program is 1.6 µs and
  **1 allocation (8 bytes)**; the VM alone (`ProgramRun`) is 1.1 µs and 0
  allocations. Before item 3 it was 2.3 µs / 5 allocations (1 KB) and
  1.2 µs / 2.
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
2. **Lock contention (F3) — done, branch `rt-task-isolation`.** The global
   scan lock is gone. Each `Program.Run` now runs against a private
   `scanView`: the program's externals (its `GlobalsDeep` set, re-derived
   after an online edit) are copied in under one read lock, the VM executes
   with no locking at all, and the changed ones are written back under one
   write lock through the same `writeLocked` path as any write, so
   generations and equal-value suppression are unchanged. What that keeps:
   every scan sees one consistent store and lands as one unit; the store
   only ever holds committed scans (`TestScanCommitsAreAtomic`). What it
   changes: tasks run concurrently, so two tasks writing one tag resolve by
   commit order (last wins, as before at scan granularity), a cross-task
   read-modify-write can lose an update exactly as on any PLC with shared
   globals, and an FB instance bound as a global in two tasks is shared
   identity those tasks race on (`New` logs a warning). `OnScan` observers
   still run with the main task's lock held, but another task may commit
   between two of their reads; one that wants an instant uses `Snapshot`.
   The main task keeps a lock of its own (`mainMu`) for its I/O buffers and
   observers. Virtual time calls scans sequentially, so it is bit-identical;
   the acceptance suites and `-race` pass.

   **Before/after on the harness (mira1 in use, 60 s, item 1 already in):**

   | run | task | before: scans ran | before: p50 / p99 / p99.9 / max | overruns / missed | after: scans ran | after: p50 / p99 / p99.9 / max | overruns / missed |
   |---|---|---:|---:|---:|---:|---:|---:|
   | B full mix | main 1 ms | 51 418 / 60 011 (85.7 %) | 6.1 µs / 264 µs / 928 µs / 2.16 ms | 610 / 8 593 | **59 974 / 60 001 (99.95 %)** | **6.8 µs / 72 µs / 624 µs / 2.48 ms** | 30 / 27 |
   | B full mix | fast2 10 ms | 5 951 / 6 001 | 8.8 µs / 5.12 ms / 9.22 ms / 9.96 ms | 132 / 50 | **6 000 / 6 000** | 9.8 µs / 86 µs / 496 µs / 1.91 ms | 0 / 0 |
   | B full mix | slow 100 ms | 600 / 600 | 9.8 µs / 68 µs / 376 µs / 372 µs | 0 / 0 | 599 / 600 | 12.5 µs / 82 µs / 1.34 ms / 1.34 ms | 0 / 0 |
   | A fast alone | main 1 ms | 60 003 / 60 005 | 6.2 µs / 31.5 µs / 344 µs / 2.53 ms | 15 / 2 | 59 992 / 60 001 | 6.1 µs / 28 µs / 312 µs / 2.37 ms | 16 / 9 |

   Run A is unchanged within noise, as it should be. In run B the 20 ms task
   no longer appears in the fast task's numbers at all; the gap that is left
   between B and A (p99 72 vs 28 µs, p99.9 624 vs 312 µs) tracks the GC the
   slow task's loop provokes (3 889 collections in 60 s against 25 in A),
   which is item 3. Raw reports:
   `docs/design/realtime/2026-10-03-mira1-prelim-after-item2/`.

   The isolation test (`TestFastTaskDoesNotWaitForSlowTask`) pins the
   property directly: a 1 ms task scanned while a ~70 ms main scan is in
   flight returns in a fraction of that time.
3. **Allocation-free scan path (F4) — done, branch `rt-alloc-free`.** The
   profile said it all: every builtin call allocated its argument slice
   (`evalExpr`, 2 per loop iteration in the slow task, 224 KB per scan),
   every user-FUNCTION call allocated a frame, every user-FB step wrapped
   its slots in a fresh frame, and `CONCAT` grew its builder piecemeal.
   Now: arguments go on a scratch stack kept on the executing `Frame`
   (reserve a window, fill, call, pop; it grows to the deepest nesting once);
   user-function frames are recycled through a `sync.Pool` per `FuncDef`
   with locals re-initialised in place (recursion and concurrent callers
   still get distinct frames); a user FB's body frame is cached on the
   instance (`FBInstance.StepFrame`, rebuilt only if a migration replaced
   the slots); `CONCAT` sizes its result once; and the main task reuses its
   output map (the `io.Driver` contract now says the map is the caller's,
   which every driver in tree already honoured).

   | benchmark (per scan) | before | after |
   |---|---:|---:|
   | `LoopScan`, 1000 iterations of `SQRT(INT_TO_REAL(i))` | 328 µs, 2 000 allocs, 224 KB | **144 µs, 0 allocs** |
   | `UserFBScan`, one user FB calling `LIMIT` | — | 0.64 µs, 0 allocs |
   | `ProgramRun` (heated tank) | 1.24 µs, 2 allocs | 1.08 µs, 0 allocs |
   | `Scan` (heated tank, loopback I/O) | 2.3 µs, 5 allocs, 1 KB | 1.6 µs, 1 alloc, 8 B |
   | `StringsScan`, 200 × `CONCAT(RIGHT(s,48), INT_TO_STRING(i))` | 91 µs, 1 078 allocs | 40 µs, 301 allocs (the strings themselves) |

   The one allocation left in `Scan` is boxing a float into the driver
   seam's `map[string]any`; it is 8 bytes and constant, and removing it
   means a typed output path on the seam, not worth it yet.

   **Before/after on the harness (mira1 in use, 60 s, items 1–2 already in):**

   | run | GC before | GC after | main 1 ms task before | after |
   |---|---:|---:|---:|---:|
   | A fast alone | 25 collections, worst pause 115 µs | **1 collection**, 49 µs | p99 28 µs, p99.9 312 µs | p99 60 µs, p99.9 656 µs (desktop noise: nothing allocates in A either way) |
   | B full mix | **3 889 collections**, 317 ms stop-the-world, worst 2.10 ms | **5 collections**, 0.6 ms, worst 197 µs | p99 72 µs, p99.9 624 µs | **p99 32 µs, p99.9 368 µs** |
   | C fast + alloc | 82 collections, worst 262 µs | 5 collections, worst 131 µs | p99 32 µs | p99 37 µs |

   Run B's fast task now matches run A's: nothing the slow or allocation
   task does reaches it any more. Raw reports:
   `docs/design/realtime/2026-10-03-mira1-prelim-after-item3/`.

   **`GOGC` / `GOMEMLIMIT` guidance.** With the scan path allocation-free,
   the collector runs for whatever else the process does (the tag API,
   Sparkplug, a historian). Measured with 50 MB/s of such churn added
   (`-churn-mb 50`, full mix, 60 s):

   | setting | collections | stop-the-world total | worst pause | heap | main task p99 / p99.9 / max |
   |---|---:|---:|---:|---:|---:|
   | `GOGC=100` (default) | 448 | 37 ms | 2.10 ms | 12 MB | 28 µs / 256 µs / 3.2 ms |
   | `GOGC=off GOMEMLIMIT=256MiB` | 12 | 0.7 ms | 82 µs | 134 MB | 29 µs / 336 µs / 2.3 ms |

   So: the fast task's p99 does not care; the worst pause and the CPU the
   collector burns do. On a controller with memory to spare, set
   `GOMEMLIMIT` to what it can have and `GOGC=off` (or a high `GOGC`): the
   collector then runs only when the heap reaches the limit. On a small
   box, leave the default — the scan loop itself no longer feeds it.
4. **Pinning — implemented, branch `rt-pinning`; measured in part.** Each
   task's loop thread is already its own locked OS thread (item 1). Now
   `Options.CPUs` / `Task.CPUs` pin it with `sched_setaffinity`, and
   `Options.Priority` / `Task.Priority` (1–99) move it to `SCHED_FIFO`
   with `sched_setscheduler`; the manifest keys are `cpu:` and `priority:`
   per task (`runtime/sched_linux.go`, `sched_other.go`; `syscall` only).
   **A refused request is loud:** `slog.Error` with the fix, and
   `ScanStats.Sched` / `TaskStats.Sched` carry `applied: false` and the
   refusal text, which the dashboards show as "cpu 3 · fifo 50 REFUSED" on
   the task row. The task then runs unpinned at normal priority — never a
   silent fallback. `SCHED_FIFO` needs `CAP_SYS_NICE` (`setcap
   cap_sys_nice+ep` on the binary, or root) or an rtprio rlimit
   (`ulimit -r`, `/etc/security/limits.conf`). Off Linux both keys are
   refused the same way. The harness takes `-cpu` / `-priority` for the
   main task and exits rather than produce a report that looks pinned when
   the OS refused.

   **What affinity alone does on a busy, non-isolated box** (mira1 in use,
   stock kernel, 60 s, fast tasks alone, items 1–3 in):

   | main task thread | scans ran | late > 100 µs | p50 | p99 | p99.9 | max |
   |---|---:|---:|---:|---:|---:|---:|
   | unpinned | 60 017 / 60 018 | 0.61 % | 10 µs | **55 µs** | 608 µs | 2.27 ms |
   | pinned to P-core 2 | 59 998 / 60 011 | 2.78 % | 6 µs | 312 µs | 1.22 ms | 2.89 ms |
   | pinned to E-core 20 | 60 000 / 60 003 | 3.27 % | 13 µs | 392 µs | 1.06 ms | 2.47 ms |
   | pinned to the idlest core at launch (25, 9 % busy) | 60 011 / 60 018 | 2.66 % | 13 µs | 408 µs | 1.06 ms | 1.98 ms |
   | full mix, pinned to P-core 2 | 59 940 / 60 001 | 3.24 % | 6.5 µs | 424 µs | 1.41 ms | 3.41 ms |

   Pinning alone made it **worse**, p99 by 6–7×: unpinned, the scheduler
   moves the task to an idle core the instant something lands on its
   current one; pinned, it waits behind whatever the desktop put there (a
   VM, an encoder), and at normal priority it has no claim to go first.
   Affinity buys cache locality and nothing else. It only pays when the
   core is kept EMPTY (kernel isolation) or the task can PREEMPT what is
   there (`SCHED_FIFO`) — and ideally both. So the manifest doc says: do
   not set `cpu:` without one of the two. Raw reports:
   `docs/design/realtime/2026-10-03-mira1-prelim-item4/`.

   **What the kernel must be told, for a pinned core to be quiet** (write
   these into the docs page when the measurements exist):

   - `isolcpus=managed_irq,domain,2-3` (or the cpuset/cgroup `isolated`
     partition on a systemd box) keeps the scheduler's load balancer off
     cores 2–3; only threads pinned there run there.
   - `nohz_full=2-3` stops the periodic scheduler tick on those cores while
     a single thread runs; `rcu_nocbs=2-3` moves RCU callbacks off them.
   - `irqaffinity=0-1` (and per-device `/proc/irq/*/smp_affinity`) keeps
     interrupt handling off the pinned cores; the fieldbus NIC's IRQ is
     the one exception worth putting NEAR the task's core.
   - Go's own threads (GC workers, the poller, every other goroutine) are
     not pinned and will use the isolated cores only if something pins
     them there — but the GC's stop-the-world still stops the pinned
     thread, pinned or not. That ceiling stands.
   - Hybrid CPUs (this i9: 8 P-cores with SMT, 16 E-cores): pin to a
     P-core, and its SMT sibling should be isolated too or the sibling's
     load halves the core.
   - `SCHED_FIFO` at priority 50 with the default `sched_rt_runtime_us`
     (950 ms of every second) leaves 5 % for everything else, which is the
     kernel's protection against a runaway real-time loop; a task that
     overruns its period continuously will be throttled, which shows up
     as missed slots.

   **With `SCHED_FIFO`** (James granted `cap_sys_nice` to the harness
   binary; same desktop, same load, 60 s, items 1–3 in):

   | main task thread | scans ran | late > 100 µs | p50 | p99 | p99.9 | max |
   |---|---:|---:|---:|---:|---:|---:|
   | unpinned, normal priority (from above) | 60 017 / 60 018 | 0.61 % | 10 µs | 55 µs | 608 µs | 2.27 ms |
   | unpinned, FIFO 50 | 60 016 / 60 017 | 0.02 % | 7.9 µs | 26.5 µs | 59 µs | 460 µs |
   | pinned to P-core 2, FIFO 50 | **60 001 / 60 001** | 0.03 % | 4.5 µs | **20 µs** | **56 µs** | **319 µs** |
   | full mix, pinned to P-core 2, FIFO 50 | 60 000 / 60 001 | 0.01 % | 2.4 µs | 14.8 µs | 27 µs | 1.92 ms |

   The priority is what pays: p99.9 drops ten-fold and the worst case
   from milliseconds to hundreds of microseconds, because the task now
   preempts the desktop's load instead of queueing behind it. Pinning on
   top of FIFO adds a little (cache locality, no migration) and costs
   nothing once the task can take the core. The full mix's one 1.9 ms
   outlier in 60 000 scans is the kind of thing an isolated core or
   PREEMPT_RT is for. This is the before/after the handoff wanted for a
   video, on a stock kernel, with no reboot: "unpinned, normal priority"
   against "pinned, FIFO 50". Raw reports in the same directory.

   **Still to measure:** the same rows on a spare box booted with
   `isolcpus`/`nohz_full`, then on a PREEMPT_RT kernel.

Honest ceiling to keep in the docs: Go's GC briefly stops every thread,
pinned or not (worst pause seen above: 5.2 ms, under an allocation-heavy
task), and on a stock kernel any thread can be preempted for milliseconds. This phase makes Nautilus **much better soft real-time**, not hard
real-time. Ethernet fieldbus jitter is separate and pinning doesn't fix it.

## Phase 3 — explore harder real-time (spikes, decide with evidence)

### The fast loop owns its I/O

James, 2026-10-05: "I don't know that a fast loop interacting solely with a
slow one does anyone a whole lot of good… the only way that would make
sense is if the fast loop had its own I/O." That is the design rule for
everything in this phase, stated here so no table row has to imply it:

- **The fast side talks to its own field devices directly** — a fieldbus,
  a serial link, or pins — and runs its loop against them. Nautilus is the
  line PLC above it: sequencing, recipes and targets, counts, rejects,
  alarms, the HMI. It hands the fast side a target and reads back results
  and faults, the way a filler controller sits under a line PLC.
- The worked example: a bottling line. The fast part talks to the
  individual fill heads, often over Modbus RTU on RS-485; Nautilus owns
  the line.
- **Serial is a first-class case, and it is about consistency, not
  speed.** RS-485 is capped by the wire: Modbus RTU at 115 200 baud is
  roughly 1–2 ms per request and response, so a real-time loop there buys
  deterministic bus timing and line turnaround, not raw rate. That favours
  the microcontroller's UART over a Linux serial port. Sub-millisecond
  fill heads are usually on EtherCAT, or pins counting a flowmeter.

So the shared-memory exchange below is the supervisor link — parameters
and setpoints down, results and faults up — not the fast loop's I/O.

**Status (2026-10-04):** the Rust fast-loop spike exists (`rt/fastloop`,
`tools/jitter/shmpeer`, `rt/README.md`) and has its first rows below; the
microcontroller spike waits for the Arduino UNO Q James sets up the week of
2026-10-06.

### Rust fast loop — first measurements

**What the spike proves, and what it does not.** It has no real I/O: its
PI block takes its process value over shared memory from the Go peer and
hands its output back the same way. The rows below therefore prove the
loop's timing and the supervisor exchange, nothing about a fast loop
driving fill heads over RS-485 or counting a flowmeter on a pin. That
part needs the I/O in the loop, which is the UNO Q spike.

Same desktop, same load, 60 s at 1 kHz, the Go peer polling the segment
every 1 ms and moving the setpoint. Go rows from Phase 2 item 4 for
comparison (slot lateness, both):

| loop | placement | scans ran | p50 | p99 | p99.9 | max |
|---|---|---:|---:|---:|---:|---:|
| Go (`tools/jitter` main task) | unpinned, normal | 60 017 / 60 018 | 10 µs | 55 µs | 608 µs | 2.27 ms |
| **Rust (`rt/fastloop`)** | unpinned, normal | 59 997 / 60 000 | 5.9 µs | 33 µs | 504 µs | 2.44 ms |
| Go | pinned cpu 2, normal | 59 998 / 60 011 | 6 µs | 312 µs | 1.22 ms | 2.89 ms |
| **Rust** | pinned cpu 2, normal | 59 986 / 60 000 | 3.8 µs | 304 µs | 1.50 ms | 2.58 ms |
| Go | unpinned, FIFO 50 | 60 016 / 60 017 | 7.9 µs | 26.5 µs | 59 µs | 460 µs |
| **Rust** | unpinned, FIFO 50 | 59 999 / 60 000 | 2.4 µs | 16 µs | 26 µs | 217 µs |
| Go | pinned cpu 2, FIFO 50 | 60 001 / 60 001 | 4.5 µs | 20 µs | 56 µs | 319 µs |
| **Rust** | pinned cpu 2, FIFO 50 | 59 999 / 60 000 | **2.1 µs** | **15 µs** | **28 µs** | **232 µs** |

Reading: **on a stock kernel the OS floor dominates and the runtime
underneath shows only in the tail.** At normal priority the two are
indistinguishable, and affinity-alone hurts Rust exactly as it hurts Go.
Under `SCHED_FIFO`, Rust is about 2× better at p50 and p99.9 (2 µs and
28 µs against 4.5 µs and 56 µs), a quarter better at p99 (15 against
20 µs), and its worst case is 230 µs against 320 µs — the same order of
magnitude, set by the stock kernel's preemption latency, not by either
runtime. What Rust removes is what these rows still cannot show on a
busy desktop: the Go collector's stop-the-world (worst seen in Phase 2:
5 ms under a churning task, 0.2 ms after item 3) and the possibility of a
Go runtime thread landing on the loop's core. Those are the rows to get
on an isolated core and on PREEMPT_RT, where the kernel's own floor drops
to tens of microseconds and the runtime's tail becomes the tail. Until
then the honest summary is: **Go with Phase 2 gives a 1 ms task 20 µs at
p99; Rust gives 15 µs; the kernel gives both a 0.2–0.3 ms worst case.**

The exchange: 0 torn reads in 55 000 supervisor reads, 0–2 stale input
reads on the loop side in 60 000 scans, and the loop's counter advancing
at 999–1000/s with the supervisor attached. A result's age when the
supervisor reads it is p50 ≈ 490 µs, p99 ≈ 1 ms: that is the supervisor's
own 1 ms poll phase, not the seqlock (a futex wake from the loop would
make it microseconds, and a supervisor scanning at 100 ms would not care
either way). Raw: `docs/design/realtime/2026-10-04-mira1-rust-spike/`.

### What runs in the fast loop — recommendation

The spike's loop is one fixed block, and that is the recommendation for
the first real version: **a fixed set of fast blocks, each bound to the
fast side's own I/O (a fieldbus, a serial line, pins), configured from
the manifest, not a second IR executor.** Nautilus supplies parameters
and setpoints as tags and reads results and faults back as tags; users
configure blocks in the manifest and never write Rust or firmware.

| option | what it buys | cost | risk |
|---|---|---|---|
| Fixed blocks in Rust (PID/PI, a counter/encoder block, a ramp/motion profile, a filter), parameters and I/O bindings from the manifest, exchanged as tags | every fast use case the poll named; same code builds for Linux (this spike) and for the UNO Q's STM32 as `no_std` | ~1 week per block with tests; the manifest/driver plumbing ~1 week once | low: each block is small, testable against the Go VM's own FBs |
| A Rust executor for the Nautilus IR with identical semantics | user logic in the fast loop | months: the IR, every builtin, every FB, cross-tested against the acceptance suites as the oracle, then kept in step forever | high: two VMs that must agree is the single most expensive thing this project could take on |
| Nothing fast in a separate process; Phase 2's Go loop pinned + FIFO | p99 20 µs on a stock kernel, today | 0 | the GC tail and the kernel's tail remain; fine for 1 ms, not for 100 µs |

So: ship Phase 2 as the answer for 1 ms class tasks; grow `rt/fastloop`
into a manifest-configured block runner for the sub-millisecond / pin-level
class, with the same block set targeting the microcontroller — and with
each block's I/O binding (Modbus RTU slave address and registers, an
EtherCAT PDO, a pin) part of the block's manifest entry.

**Open question:** what happens when the block a line needs is not on the
menu. Options range from "it is a Nautilus task at 1 ms, which Phase 2
made good" through "a block request to us" to "an escape hatch into Rust
for the customer"; undecided, and the answer decides how big the block
set must be before this ships.

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
- **"A slow task no longer delays a fast one: with a 20 ms task every 100 ms
  beside a 1 ms task, the fast task keeps 99.95 % of its scans and a 72 µs
  p99, where the old global scan lock left it 59 % and 27 ms"** — evidence:
  item 2's table (and run B before item 1 for the 59 % / 27 ms). The second
  before/after story (N-73). State the shared-globals rule honestly: last
  commit wins, as on any PLC.
- **"Pinning a task to a core on a stock kernel, with nothing else
  changed, makes it worse — p99 went from 55 µs to 300–400 µs on a busy
  desktop — and Nautilus tells you when a pin or priority was refused
  instead of pretending"** — evidence: item 4's first table. A useful,
  honest thing to say in the poll follow-up: "pinned cores" is not a
  setting, it is a setting plus a kernel configuration.
- **"With `priority: 50` and `cpu: 2` in the manifest, on a stock Linux
  kernel on a busy desktop, a 1 ms task ran 60 001 of 60 001 scans with
  p99 20 µs, p99.9 56 µs and a worst case of 0.32 ms — and needs one
  `setcap` to do it"** — evidence: item 4's second table. The video shot:
  that row against "unpinned, normal priority" (p99 55 µs, worst 2.3 ms).
- **"A Rust loop with no runtime underneath, same box, same priority, is
  15 µs at p99 against Go's 20 µs and 28 µs at p99.9 against 56 µs; the
  worst case is the kernel's either way, 0.2–0.3 ms on a stock kernel"**
  — evidence: the Phase 3 table. This is the number that answers the
  poll's "Rust process" option honestly: a real but modest gain on a
  stock kernel, whose value shows on PREEMPT_RT.
- **"The scan loop allocates nothing: a 1 000-iteration loop with two
  builtin calls per iteration makes zero allocations per scan, and a
  loop-heavy task beside a 1 ms task went from 3 889 collections a minute
  to 5"** — evidence: item 3's tables. Say "the scan path"; the rest of the
  process (API, MQTT) still allocates, which is what `GOMEMLIMIT` is for.

## Open items

- **Baselines not yet run** (James, 2026-10-03): the office cluster nodes
  are out — they are in service. No idle desktop yet. James has spare Linux
  boxes (a Tensor PC and VP6650s) that can be hooked up in a few days: those
  are the stock-kernel industrial-PC baseline, and candidates for a
  PREEMPT_RT kernel since they are not shared. ARM waits for an ARM Linux
  board. A Mac mini (Apple silicon) is available: useful as a quick arm64
  check of F1 (Go's ticker behaviour), not as a deployment baseline.
- The isolated-core and PREEMPT_RT comparisons need a box booted with
  `isolcpus=`/`nohz_full=` and then a PREEMPT_RT kernel — the spare boxes
  are the candidates; nobody else runs on them.
- A `jitter.yml` workflow runs the harness on a hosted runner nightly and on
  demand (relative numbers only, plus a ≥ 99 % scans-ran guard on the fast
  task) and uploads the reports; add the self-hosted boxes' labels to its
  matrix once they are runners — that is the baseline rig.
- `hmi` package: `types.ts` grew a `Lateness` interface and `ScanDiagnostics`
  shows it; a patch bump will be wanted when it ships (publish-on-bump).

## How to work

- Everything above is on `main`; branch off it per item (worktree
  `../nautilus-rt` is the working checkout).
- Ask James before buying hardware (a UNO Q, a logic analyzer) or installing an
  RT kernel on a shared machine, or running load on an in-service node.
- Report back in plain terms: what you measured, what changed, what it means.
