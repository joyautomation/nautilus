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
  1 Hz heartbeat on DO16 output 15 shows in the shim's own view of the
  frame (`naut plcnext shm --watch`, and the shim's log): output byte 2
  toggles `00`/`80` every 500 ms, generation applied = generation
  written;
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

Lateness, best-effort (SCHED_FIFO refused, below): on a quiet controller
about 1 cycle in 1 500 is later than 200 µs, the same as stage 1. It is
bursty, and the bursts are other load: a 20 MB scp over the controller's
Ethernet (the NIC's receive thread runs at RT priority) put 250–1 400
late cycles into a 10 s window, and naut at a 5 ms scan raised the rate
to about 2 % with a worst of 12 ms and a few dozen torn reads of
nautilus's block (the shim kept the previous outputs each time, as
designed). The 50 ms scan stays the bench's setting; SCHED_FIFO for the
shim is what isolates it from both.

Not yet: a human confirming the DO16 LED blinks (the frame is written;
the module's UP supply decides whether a channel switches), and
SCHED_FIFO numbers.

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

```sh
# any recent zig; the controller is glibc 2.35 armhf
zig cc -target arm-linux-gnueabihf.2.35 -mcpu=cortex_a9 -O2 -std=c11 -Wall -Wextra \
       -o nautilus_io rt/plcnext-io/nautilus_io.c -ldl -lpthread

ssh admin@<plc> 'mkdir -p /opt/plcnext/apps/nautilus/io/logs && chmod 777 /opt/plcnext/apps/nautilus/io /opt/plcnext/apps/nautilus/io/logs'
scp nautilus_io rt/plcnext-io/NautilusIo.acf.settings admin@<plc>:/opt/plcnext/apps/nautilus/io/
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

`sched_setscheduler(SCHED_FIFO)` is refused: the firmware's own
`Arp.System.Application` carries file capabilities
(`cap_sys_nice`, …) and its children do not inherit them. The ESM tasks
run at FIFO 97–98 in that process. Giving `nautilus_io` the same needs
`setcap cap_sys_nice+ep nautilus_io` as root, and `admin`'s sudo on a
PLCnext is an allowlist without `setcap`. Until that is solved the
cycle is best-effort, which the numbers above describe honestly; with
it, the sample runtime's 1 ms cycle with sub-100 µs jitter is the
expectation.

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
