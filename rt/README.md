# rt — harder real-time spikes (Phase 3)

Experiments that live beside the Go build and never touch it: a Rust fast
loop, and (next) a microcontroller firmware. Everything here is measured
with the same lateness statistics as `tools/jitter`, so the rows sit in one
table in `docs/design/realtime.md`.

## fastloop

`rt/fastloop` is a 1 kHz loop with no runtime underneath it: one thread,
pinned and `SCHED_FIFO` when asked, woken by `clock_nanosleep(TIMER_ABSTIME)`
on an absolute schedule, running one fixed block (the same PI loop the Go
harness's fast task runs) on tags exchanged with the supervisor through a
4 KB shared-memory segment. The one dependency is `libc`.

```sh
cd rt/fastloop && cargo build --release
./target/release/nautilus-fastloop --duration-s 60 --cpu 2 --priority 50 --json out.json
go run ./tools/jitter/shmpeer -duration 59s        # the supervisor side, from the repo root
```

`SCHED_FIFO` needs `CAP_SYS_NICE` on the binary (`sudo setcap cap_sys_nice+ep
target/release/nautilus-fastloop`); a refused placement exits 2 rather than
report numbers that look pinned.

### The exchange

Two seqlocks in one page (layout in `src/main.rs`): `in` is written by the
supervisor (setpoints, gains) and read by the loop; `out` is written by the
loop (results, its scan counter, a `CLOCK_MONOTONIC` stamp) and read by
the supervisor. The loop never blocks on the other side: writing `out` is
two atomic bumps around the stores, and reading `in` retries a bounded
number of times then keeps its last values and counts a `stale`. The
supervisor retries a torn read until it is clean — it is the one with time
to spare. `shmpeer` (Go, stdlib `syscall.Mmap`) is that supervisor: it
drives a moving setpoint, reads results, and reports how old a result is
when read, torn reads, and whether the loop's counter advances at its rate.

In Nautilus proper this would be an `io.Driver`: the loop's `out` values
are the supervisor's inputs, its `in` values the supervisor's outputs, read
and written once per supervisor scan like any field bus — which is also why
a result's age at the supervisor is bounded by the supervisor's own period,
not by the exchange.

### What it is for

The spike answers "what does the OS give a loop with nothing underneath
it, on this box, with this kernel", next to the Go loop after Phase 2. It
does not run IEC logic. See the design doc for what would.
