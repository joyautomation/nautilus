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
- 1 ms cycle: reads the first input bytes and logs them on change —
  they match the GDS mirror the gRPC path reads byte for byte
  (`00 00 00 00 80 20 80 20`); writes a 1 Hz heartbeat on the DO16
  module's output 15 as the frame's only writer;
- cost: ~2 % of one core, 17 MB resident; lateness at 1 ms best-effort
  (SCHED_FIFO refused, see below): about 1 cycle in 1 500 later than
  200 µs, worst 1.7 ms over 80 000 cycles with the bus running.

Not yet: the shared-memory exchange with the Go runtime (stage 2), and
a human confirming the DO16 LED blinks (the frame is written; the
module's UP supply decides whether a channel switches).

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

## Stage 2

`nautilus_io` becomes the I/O side of the plcnext driver: it exchanges
the two frames with the Go runtime through the seqlock page
`rt/fastloop` already defines (inputs out, outputs in, a scan counter
and a monotonic stamp), and the driver gains `transport: shm` beside
the gRPC path. The manifest, bindings and tag file do not change; only
the bytes' route does.
