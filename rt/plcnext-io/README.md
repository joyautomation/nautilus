# rt/plcnext-io — the in-process tier for PLCnext

`nautilus_io` is a PLCnext "runtime" process in Phoenix Contact's own
sense (their `SampleRuntime`): the firmware's ACF starts it beside the
plcnext process, it joins the firmware as a module application, and it
touches the Axioline process image through the ANSI-C API — the GDS
frame buffers `1:IN` / `1:OUT` of `Arp.Io.AxlC` — inside its own cycle,
the way the eCLR's tasks do. No socket, no serialisation, no GDS
connector: the image at the bus rate, from C, with lateness measured.

It is built **without the PLCnext SDK**. Everything it calls is a C
export of a library the firmware ships:

| library (on the controller) | entry points | role |
|---|---|---|
| `/usr/lib/libArp.System.Module.so` | `ArpSystemModuleMain2(name, settingsPath)` | what the SDK's static `ModuleLib` wraps as `ArpSystemModule_Setup`; returns the app domain |
| `/usr/lib/libArp.Plc.AnsiC.so` | `ArpPlcIo_GetBufferPtrByBufferID`, `ArpPlcGds_GetVariableOffset/BitOffset`, `ArpPlcGds_Begin/EndRead`, `Begin/EndWrite`, `ArpPlcIo_ReleaseGdsBuffer`, `ArpPlcDomain_SetHandler`, `ArpPlc_GetLastError` | the I/O surface; loaded INTO this process by the ACF, never by this code |

Found by reading the firmware's own binaries (the libraries' dynamic
symbol tables, and one small decompile of `libArp.System.Module.so` to
see that `ArpSystemModuleMain2` is `ModuleLibrary::Main` plus the app
domain assignment). The signatures are hand-declared from how
`PLCnext/SampleRuntime` calls them.

## Status (2026-10-24, bench AXC F 2152, firmware 2023.0)

Stage 1 — prove the path — is done:

- joins the ACF (`Process 'NautilusIo' started successfully`), waits for
  the ACF to load the ANSI-C library and for the PLC domain's first
  `Start*` callback, acquires `1:IN`, `1:OUT` and `DiagVars`
  (`DI4096`/`DO4096` at offset 0, `AXIO_DIAG_STATUS_REG_RUN` at 1.5);
- 1 ms cycle: reads the input frame — byte for byte what the GDS mirror
  the gRPC path reads (`00 00 00 00 80 20 80 20`); is the output frame's
  only writer;
- cost: ~2–3 % of one core, 17 MB resident.

Stage 2 — nautilus's I/O through it — is done and running on the bench:

- every cycle the shim publishes the input frame, the bus RUN bit (read
  from `DiagVars` every cycle; it rises at the BusConductor handshake
  exactly when the gRPC `AXIO_DIAG_STATUS_REG_RUN` does), its stamp and
  lateness counters, and the bytes it wrote to the output frame, into
  the exchange page; and writes into the output frame what nautilus left
  in the page — zeros before nautilus has written, and zeros again once
  nautilus's outputs are older than its watchdog (`output-watchdog`,
  default 1 s);
- the plcnext driver's `transport: shm` reads and writes that page
  (`plcnext/shm.go`); `examples/plcnext-bench` runs on it. bench.st's
  1 Hz heartbeat blinks the DO16's output 16 LED and shows in the shim's
  own view of the frame (`naut plcnext shm --watch`, and the shim's
  log): output byte 1 toggles `00`/`80` every 500 ms, generation applied
  = generation written;
- killing naut (`kill -9`) drops the frame to zero 1 s later, in the
  shim's log as `nautilus's outputs are older than 1000 ms`; a runtime
  restart restarts the shim, and naut sees the new start stamp, ends its
  session and runs the BusConductor handshake again by itself.

| bench, `examples/plcnext-bench` | transport grpc | transport shm |
|---|---:|---:|
| route per scan | 2 gRPC round trips | 2 copies through the page |
| time per scan for the image | ~11 ms | 0.03–0.05 ms |
| input age when naut reads it | — (the mirror, refreshed every 500 µs, plus the RPC) | 0.1–1.1 ms |
| naut CPU at a 50 ms scan | 9 % | 4.0 % |
| naut CPU at a 5 ms scan | — (the RPC alone is 11 ms) | 21.5 % |
| shim CPU | — | 2.8–2.9 % |
| naut resident | 23 MB | 20 MB |

Lateness, best-effort (SCHED_FIFO refused, below), measured with
`naut plcnext bench` (2026-10-24, 3–5 min windows, naut at a 50 ms
scan, late = more than 200 µs after the shim's 1 ms deadline):

| shim / naut | quiet | two busy loops | stream over ssh |
|---|---:|---:|---:|
| both free (the firmware's default) | 0.2–2.8 % late, worst 5–13 ms | 2.2 %, worst 4.3 ms | 66.5 %, worst 25 ms |
| naut stopped | 0.12 %, worst 0.25–2.4 ms | | |
| shim CPU 0, naut CPU 1 | 16 %, worst 5.2 ms | 19 %, worst 1.3 ms | 33 %, worst 9.5 ms |
| **shim CPU 1, naut CPU 0** | **0.13 %, worst 2.6 ms** | **0.20 %, worst 0.95 ms** | **33 %, worst 9.6 ms** |

"Worst" is the worst 10 000-cycle window the bench saw. Percentiles are
in `bench.jsonl`; for the pinned row, p99 ≤ 200 µs and p99.9 ≤ 500 µs
quiet and under the busy loops.

What it says. On a two-core controller the shim's lateness is mostly
who shares its core. Left alone the scheduler keeps the shim's cycle on
CPU 1 and lets naut's threads wander onto it; how often they do is why
the unpinned quiet runs range from 0.2 % to 2.8 %; the "1 cycle in
1 500" this page quoted before came from single 10 s windows and does
not hold up. CPU 0 carries the
firmware's periodic RT tasks (one ESM core, `T_Cyclic100`, the
Profinet timers), and a best-effort shim there is late one cycle in
six. CPU 1 carries the other ESM core and the network receive path
(`stmmacRxTsk` at FIFO 95, eth0's IRQ thread at 50), so pinning
best-effort cannot help much under heavy traffic: two busy loops are
shut out, an ssh stream is not. That last column is what SCHED_FIFO is
for. The bench pins both (`nautilus_io.cpu`, `naut.cpu`, below).

With SCHED_FIFO (after the `setcap` below; shim on CPU 1, naut on CPU 0
with its scan task at FIFO 40, same 3 min windows):

| shim | quiet | two busy loops | stream over ssh |
|---|---:|---:|---:|
| pinned, best-effort (above) | 0.13 % late, worst 2.6 ms | 0.20 %, worst 0.95 ms | 33 %, worst 9.6 ms |
| FIFO 50 | 0 late; p99 ≤ 50 µs, worst 55 µs | 0 late (1 over 100 µs); worst 118 µs | 0.21 % in one run, 20 % in the next; worst ≤ 1–2 ms |
| **FIFO 80** | **0 late; p50 ≤ 10 µs, p99 ≤ 20 µs, worst 51 µs** | **0 late; p99.99 ≤ 100 µs, worst 154 µs** | **3.2 % in both runs; p99 ≤ 500 µs, worst ≤ 1 ms** |

naut's own scan at FIFO 40 stays within 500 µs of its deadline quiet and
under the busy loops (the FIFO 50 busy-loop run had a worst scan of
≤ 10 ms), and
within 1–2 ms under the stream.

So the shim's default is 80. At 50 it ties with eth0's interrupt thread,
and between equal FIFO priorities whoever runs first finishes first:
the same load, measured twice, gave 0.2 % and 20 %. At 80 the result
repeats to the hundredth of a percent, and what is left under a stream
is the network receive task (`stmmacRxTsk`, FIFO 95) on the same core,
which nothing under the ESM outranks. Nothing is late by more than a
millisecond in any FIFO run: the 1 ms cycle holds; the 200 µs line does
not, while someone streams into the controller's Ethernet.

Seen with eyes (2026-10-27): the heartbeat LED, and the fail-safe
taking it dark within a second of naut being killed.

## Three things the bench taught, each the hard way

1. **A declared ACF process must never exit during start-up.** The
   first version exited after one error and the firmware answered
   `Could not start all processes` — MainProcess FATAL, hardware
   watchdog, controller reboot, PLC `Ready|Blocked` until the next
   clean restart. So `nautilus_io` never exits on its own: every failure
   is logged and the process idles until the ACF ends it.
2. **The ANSI-C library is loaded before the component behind it
   exists.** A call in that window throws an `Arp::Exception` through
   the C frame — `terminate`, abort, and the same cascade as above. The
   PLC operation callback (`ArpPlcDomain_SetHandler`) is delivered by
   that component, so its first `Start*` is the proof it is alive; no
   I/O call happens before it, and the cycle skips I/O while the PLC is
   not started.
3. **A guessed signature segfaults inside the firmware.**
   `ArpPlcGds_GetLayoutMemorySize` is not what one argument suggests;
   it is not called. Everything that is called has a sample-code
   precedent.

Each lesson cost one runtime restart and, once, a controller reboot.
Test a change by watching `logs/nautilus_io.log` for the step it dies
on; the firmware's `Output.log` says `exited unexpectedly` and little
else.

## Build and deploy

A CLI release attaches the build as
`nautilus-plcnext-io_<version>_linux_armv7.tar.gz` (with a `.sha256`
beside it): `nautilus_io`, the two ACF files and this README, under one
directory. Unpack it and run the copy steps below from inside that
directory, with the `dist/plcnext-io/` and `rt/plcnext-io/` prefixes
dropped. To build it instead
(zig on PATH, or `ZIG=`; the release uses zig 0.17.0):

```sh
rt/plcnext-io/build.sh            # → dist/plcnext-io/nautilus_io, version "dev"
VERSION=0.19.0 rt/plcnext-io/build.sh -p   # also the release tarball + .sha256
```

It cross-compiles for the controller's glibc 2.35 armhf with warnings
as errors, and stamps the version into the log's first line
(`nautilus_io <version> starting …`). Then, from the repo root:

```sh
ssh admin@<plc> 'mkdir -p /opt/plcnext/apps/nautilus/io/logs && chmod 777 /opt/plcnext/apps/nautilus/io /opt/plcnext/apps/nautilus/io/logs'
scp dist/plcnext-io/nautilus_io rt/plcnext-io/NautilusIo.acf.settings admin@<plc>:/opt/plcnext/apps/nautilus/io/
scp rt/plcnext-io/NautilusIo.acf.config admin@<plc>:/opt/plcnext/projects/<current project>/
ssh admin@<plc> 'chmod 755 /opt/plcnext/apps/nautilus/io/nautilus_io; sudo /etc/init.d/plcnext restart'
```

The ACF includes `$ARP_PATH_PROJECT_CURRENT_DIR$/*.acf.config`, so the
`.acf.config` in the current project directory is enough; it runs the
binary as the firmware's user (`plcnext_firmware`), which is why the
`io/` directory is world-writable for the log. The output frame must
have one writer: the project's `PCWE.gds.config` has no connector into
`Arp.Io.AxlC/0.DO4096` once this process runs
(`examples/plcnext-bench/controller/`). To take it out again, move the
`.acf.config` aside and restart.

## Real-time priority

Out of the box `sched_setscheduler(SCHED_FIFO)` is refused: the
firmware's own `Arp.System.Application` carries file capabilities
(`cap_sys_nice`, …) and its children do not inherit them. The ESM tasks
run at FIFO 97–98 in that process. `nautilus_io` needs the same file
capability, and `admin`'s sudo on a PLCnext is an allowlist without
`setcap`, but it has `passwd`, which is Phoenix's documented way to a
root login. So, once per binary, typed by the controller's owner:

```sh
ssh -t admin@<plc> 'sudo passwd root && su -c "setcap cap_sys_nice+ep /opt/plcnext/apps/nautilus/io/nautilus_io; setcap cap_sys_nice+ep /opt/plcnext/apps/nautilus/naut; getcap /opt/plcnext/apps/nautilus/io/nautilus_io /opt/plcnext/apps/nautilus/naut"; sudo passwd -l root'
```

sets a root password, uses it once, and locks root again. A capability
belongs to the file: copying a new build into place drops it, so the
step is part of every deploy of either binary that should run FIFO.
Without it both run best-effort and say so (the page's `NIO_ST_FIFO`,
the log's `refused`, naut's `scan.sched`). naut needs it only for a
task with `priority:`; the bench's scan task is at 40 once it is set.

The shim asks for priority 80 — over the firmware's interrupt threads
(50), under `T_Cyclic100` (82), its network receive task (95) and the
ESM (97–98); the FIFO table above is why not 50. A `nautilus_io.prio`
file beside the binary holding one number (1–96) asks for another; the
page reports what was asked (`NIO_OFF_PRIO`) and whether it was granted
(`NIO_ST_FIFO`).

A `nautilus_io.cpu` file beside the binary pins the cycle to one CPU;
that needs no capability. Use CPU 1, with naut on CPU 0 (`naut.cpu`
beside naut, read by `naut-keepalive.sh`): the table above is why.

To compare, `naut plcnext bench` on the controller measures one window
from the page and naut's `/api/state`: the shim's lateness histogram
(cumulative in the page, so a window is a difference), its worst
10 000-cycle window, naut's scan lateness, and CPU.
`examples/plcnext-bench/controller/bench-matrix.sh <mode> 5m` runs it
quiet, under two busy loops, and under a stream over ssh, appending JSON
lines to `/opt/plcnext/apps/nautilus/bench.jsonl` with the CPUs
each side ran on; run it once best-effort and once after the `setcap`.

## The exchange page

`nautilus_io_shm.h` is the one definition; `plcnext/shm.go` mirrors it
and a Go test reads the header and fails if they drift. One 4096-byte
file, `/dev/shm/nautilus-plcnext-io`, little-endian, rt/fastloop's
seqlock shape — two blocks, one writer each, sequence odd while
writing, readers copy between two equal even reads with bounded
retries, nobody waits:

| offset | block | writer | what |
|---|---|---|---|
| 0 | header | shim, once | magic `NAUTPNIO`, version, period, frame length, pid, start stamp, late threshold |
| 64 | inputs | shim, every cycle | status (PLC started, frames, RUN known, RUN, FIFO, watchdog), cycle stamp, input-frame stamp, cycles, late, worst, last, generation applied, torn |
| 128 | | | input frame, 512 bytes |
| 640 | | | the output frame as the shim wrote it this cycle, 512 bytes |
| 2048 | outputs | naut, every scan | length owned (from 0; 0 = never written), stamp, generation, watchdog ms |
| 2112 | | | output bytes, 512 |

The shim creates the file 0666 as `plcnext_firmware` — the firmware's
own `/dev/shm` segments are made the same way, and `admin` is in that
group — and keeps nautilus's block across its own restart. naut never
creates it: no page means no shim. Stamps are `CLOCK_MONOTONIC`, one
clock for both. `naut plcnext shm [--watch 250ms]` prints the page
without writing it.

Quality on the Go side: Good while the shim's stamp and input frame are
under 100 ms old and the PLC is started; Bad while the shim reads bus
RUN false; Stale when it stops cycling (the mapping survives, so it
recovers by itself); a new start stamp — the runtime restarted — ends
the session so the handshake runs again.
