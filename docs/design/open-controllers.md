# Design brief: nautilus on open controllers — groov EPIC and PLCnext

Status: **groov EPIC built and verified on the bench; PLCnext built and
verified against a fake firmware, one restart away from the bench.**
Written 2026-10-23 on the `plcnext-groov` branch (worktree
`~/Development/joyautomation/nautilus-plcnext-groov`). Context: this is
**Field drivers** work (`optommp/` and `plcnext/` are drivers, `optommp/`
also the importer) plus two bench projects; nothing above the `io.Driver`
seam changed.

**The question asked:** two controllers are on the office network, an
Opto 22 groov EPIC and a Phoenix Contact PLCnext. Can nautilus use them as
a *target* for running IEC 61131-3 — through the nautilus runtime, or
through theirs if it is open?

**The short answer:** yes, through the nautilus runtime, differently on
each. Neither vendor's own IEC engine is usable as a target (§1). The EPIC
is nautilus I/O through the processor's stock firmware: it exposes the
chassis over OptoMMP, the `optommp` driver polls it — or has it stream
its inputs over UDP, as Opto's own CODESYS driver does — with per-channel
quality (first through a Modbus projection, since superseded), and a 1 Hz
heartbeat written by an ST program running on the EPIC itself was read
back off the EPIC's own output (§2). The PLCnext is
the stronger story — the nautilus runtime running *on* the controller,
driving its Axioline modules, with Phoenix Contact's IEC engine idle — and
it is built, tested, and cross-compiled; what remains is one documented
restart of the controller's runtime that the build session was not allowed
to perform (§3). §4 says what each path is worth and what to do next.

---

## 1. "Theirs, if it is open" — it is not

| Controller | Its IEC engine | Headless? | Verdict |
|---|---|---|---|
| groov EPIC GRV-EPIC-PR1 | CODESYS Control (licensed runtime) or PAC Control (proprietary flowcharts) | CODESYS: `--runscript` on a Windows seat, PLCopenXML in; runtime license per EPIC. PAC Control: no. | A nautilus → PLCopenXML export could feed CODESYS, but the runtime is a paid, closed binary and CODESYS was not even installed on this unit (port 1217/11740 closed). Not a target. |
| PLCnext AXC F 2152 | eCLR, programmed from PLCnext Engineer (free, Windows) | PLCopenXML import exists; no automation API worth building on (vendor-research note, 2026-09-25) | Not a target. But the *platform* is open: SSH, a documented gRPC data-access API, and Phoenix Contact's own replacement-runtime samples. |

So "the runtime" means the nautilus runtime in both cases, and the work is
I/O.

## 2. groov EPIC — nautilus I/O with the stock firmware

### 2.1 What the bench unit is

`192.168.82.243`, `GRV-EPIC-PR1`, four modules. Ports open: 80/443 (groov
Manage), **502 Modbus/TCP**, **2001 OptoMMP**, 8088 (Jetty, which that first scan took for Ignition
Edge), 22001 (PAC Control engine). 22 was closed on the first day; the shell
license was already on the unit and James enabled the shell server and a
shell user on 2026-10-24. Ignition Edge is not on the unit: on 2026-10-25
the shell found no Ignition service or install and nothing listening on
8088. What runs beside nautilus is Opto's: `mmpServer` (the I/O server,
≈ 10 % CPU), the PAC Control engine and its REST bridge (2–4 % CPU),
groov View with the local-display browser, Node-RED (idle), and groov
Manage. From the shell: quad-core ARMv7 (Cortex-A9
class, VFPv3/NEON), Opto groov Linux 4.1.3 (Yocto kirkstone), kernel
5.15.91 PREEMPT_RT, 1.8 GB RAM, 4 GB free on `/`, sudo with password.

| slot | module | type code | channels |
|---|---|---|---|
| 0 | GRV-IACDCTTL-24 | `0x50000005` | 24 digital inputs |
| 1 | GRV-ODCI-12 | `0x90000015` | 12 digital outputs |
| 2 | GRV-IICTD-12 | `0x6000001F` | 12 ICTD temperature inputs, °C (channel 0 wired: 20 °C) |
| 3 | GRV-IV-24 | `0x60000002` | 24 voltage inputs at ±160 V (channel 0 wired: 8.4 V) |

### 2.2 Two protocols, one driver

**OptoMMP** (TCP 2001; form 1465) answered block reads **without any
credential**: a 16-byte IEEE 1394-style header and a block at a 32-bit
address. The chassis is self-describing there:

| area | address | what |
|---|---|---|
| unit description | `F0300080` | `GRV-EPIC-PR1` |
| channel configuration | `F0100000 + slot*0x3000 + ch*0xC0` | +0 module type, +4 channel type, +8 feature |
| packed values | `F1000300` | 16 × 32 × float / uint |
| packed digital state | `F1000B00` | 512 bits |
| packed channel-present | `F1000BC0` | 512 bits |

Block reads of 0x600 bytes answer in 0.6 ms; 0x800 hangs the request
(the guide says "about 1400 bytes or less").

**Modbus/TCP** (TCP 502; form 1678) is a fixed projection of the same map,
64 channels per module whatever the module holds, 0-based PDU addresses:

| | table | address |
|---|---|---|
| digital state | coil / discrete | `512 + slot*64 + ch` |
| analog value | input register, float32 BE | `4608 + (slot*64 + ch)*2` |
| analog output | holding register, float32 BE | `1030 + (slot*64 + ch)*2` |

Cross-checked live: slot 2 channel 0 read 19.72 °C at input register 4864
and 20.19 °C over OptoMMP; slot 3 channel 0 read 8.40 V both ways.

**Decision, first pass (2026-10-23): OptoMMP for discovery, Modbus for
scan.** The runtime already had a Modbus driver with block planning,
quality, status rows, a bench slave and CI, so the importer projected the
chassis onto the processor's Modbus map (`naut optommp import --modbus`
still does) and the project said `driver: {type: modbus}`.

**Decision, second pass (2026-10-24, James): an OptoMMP driver.** Two
findings turned it. Every OptoMMP request costs ≈ 0.75 ms p50 / 1.5 ms
p99 inside `mmpServer` whatever its size, the same on loopback as over
the LAN, and the processor's Modbus server sits behind the same server —
so Modbus buys no speed, only a second translation. And OptoMMP carries
what Modbus cannot: per-channel quality. The driver (`optommp/driver.go`,
`driver: {type: optommp}`) reads, per scan, only the packed areas its
bindings need — two requests for an ordinary chassis:

| area | address | per scan |
|---|---|---|
| packed values (analog EU, digital feature value) | `F1000300 + slot*0x80` | analog / counter slots |
| expanded digital packed data: state, **quality mask**, on-latch, off-latch | `F1001900 + slot*0x10` | digital slots |
| quality indicator codes | `F1002080 + slot*0x100 + ch*4` | only if `quality` tags are bound |
| scratchpad bits / int32 / float | `F0D80000` / `F0D81000` / `F0D82000` | only if bound |

and writes when outputs change: digital outputs through the **must-on /
must-off** masks (`F1001A00 + slot*8`, one write for every commanded
channel on every module; zero bits leave a channel alone — measured, the
guide contradicts itself on this), analog outputs at `F02A0000 +
slot*0x1000 + ch*0x40`, scratchpad elements in place and scratchpad bits
through their on/off masks (`F0D80400`). On-/off-latch tags read the
packed latch mask and clear only a latch they saw set, by reading its
quadlet in the read-and-clear area (`F02E0000 + slot*0x600 + ch*0x18`,
+4 on, +8 off) — so a pulse shorter than the scan is delivered once.
Module types and the channel-present mask (`F1000BC0`) are re-read every
5 s; the power-up-clear flag (`F0300004`) is checked on connect and the
PUC sent (`F0380000`) when owed.

**Custom data area, streaming, watchdog (2026-10-24, evening).** Opto's
own CODESYS I/O driver turned out to be plain IEC code talking OptoMMP to
`127.0.0.1:2001` (§2.4 on `plcnext-groov`); it uses three features the
first driver did not, now manifest options, each off unless named
because each writes the processor's configuration:

| option | areas | what it does |
|---|---|---|
| `custom-area: true` | pointers `F0D50000` (1024), data `F0D60000` | every word a scan needs becomes a pointer, so a scan is one read |
| `stream: 20ms` | `F03FFFC0`: +8 start, +C size (≤ 1480 B), +10 on, +14 interval, +18 port, +20 target #1 | the processor pushes that area over UDP to the driver's address on the connection; inputs cost no request |
| `watchdog: 500ms` | set `F0380010`, read `F0300054`; per channel `+0x24` value, `+0x28` enable | outputs with a watchdog value go to it after that long without a request; `0s` turns it off, absent leaves it alone |

A datagram is a 2-byte length (header included), a zero, `0xAn` (n
counts up), the start address, then the data; the driver takes it only
from the processor's address. Five missed intervals (at least 100 ms)
make every tag Stale until the next datagram; none for the request
timeout ends the session and the reconnect configures the stream again.
While streaming, the only TCP traffic is writes, latch clears and the
5 s chassis check — and, with a watchdog set, a read every third of its
time to keep it fed. A clean stop puts the pointers and the streaming
configuration (stored to flash) back as first found; the watchdog stays
as set, since a stopped controller is what it is for. The pointers are
read back every 5 s: another client rewriting them would otherwise hand
the driver its data.

The bench found one trap. **A pointer into the packed values costs
mmpServer ≈ 0.5 ms every time it is resolved**: 43 pointers (36 analog)
read in 17.7 ms, and streamed every 20 ms they held mmpServer at 90 % of
a core. Pointers into the digital packed data, scratchpad or module
configuration cost nothing measurable — and so do pointers into each
channel's *expanded* read area, which returns the same value, NaN
included: analog `F0260000 + slot*0x1000 + ch*0x40`, digital feature
value `F01E0000 + slot*0x600 + ch*0x18 + 0x10`. So a pointer names those
(`optommp/plan.go`); a packed poll still reads the packed values, where
one block read is cheap. (Pointers into the unit status area at
`F0300000` cost 33 ms each — not used.) Every binding decodes from one
word image however it was filled, so all three modes deliver the same
values and the same quality.

Quality per tag: **Bad** for an empty slot or one holding a module the
manifest does not name, a digital channel the module lacks (present mask)
or whose quality bit is set, and a NaN analog (the processor reports
every bad-quality analog reading as NaN; unwired ICTD channels carry
code 9); **Stale** after the socket drops; **NotConnected** until first
read. On the bench, slots without a digital module read quality mask
`FFFFFFFF` in the digital area and digital channel-present bits are the
only ones set (analog modules set none) — the driver applies each check
only where it means something.

```
optommp/
  wire.go       read/write block over TCP 2001 (pure stdlib)
  modules.go    form 1465's groov I/O table as data: module type → part + channel count,
                channel type → range/unit; Kind from the family byte (0x50/0x54 DI, 0x90/0x94 DO, 0x60 AI, 0xA0 AO)
  chassis.go    Scan: one read per slot + the packed areas → Chassis{Modules{Channels}}
                (type, feature, configured scale and units, name, present, value)
  memmap.go     the areas the driver polls and writes
  manifest.go   optommp_manifest.yaml: host, port, scan-rate, custom-area, stream, watchdog,
                modules, bindings (points di do ai ao counter on-latch off-latch quality
                scratch-bit/-int/-float)
  plan.go       the words a scan needs: packed reads, or pointers for the custom area
  driver.go     the io.Driver
  stream.go     watchdog, pointers and stream configuration; the stream loop
  generate.go   Chassis → manifest + tag file
  modbusmap.go  Chassis → modbus.Manifest + tag file on the form 1678 addresses (--modbus)
  optommptest/  the bench chassis as an in-process device, for tests
cmd/naut/optommp.go   naut optommp browse | import [--custom-area] [--stream D] [--watchdog D] [--modbus]
```

`naut optommp import --host <epic>` writes `optommp_manifest.yaml` and
`tags/optommp.yaml`; the project says `driver: {type: optommp}`. Tag
names are `<id>_M<slot>_<Kind><ch>` — `EPIC_M1_DO11` — with or without
`--modbus`, so a project moves between the two untouched. Optional tags:
`_Count` (a digital channel with a feature configured), `--latches`
(`_OnLatch`, `_OffLatch`), `--quality` (`_Quality`), `--scratch-bits
/-ints/-floats N` (`<id>_SP_B0` …). Outputs are read back, so a DO tag
shows what the module is doing.

### 2.3 Verified

`examples/epic-bench/`: `bench.st` mirrors eleven inputs to eleven
outputs, heartbeats output 11 at `BlinkPeriodS`, names the two wired
analogs. `naut check` and `naut test` pass offline. With `naut run` up
(100 ms scan, four Modbus blocks per cycle), the EPIC's coil 587 read
directly off the processor toggled `1100110011000110` over four seconds.
The output was switched back off afterwards.

**Nautilus on the EPIC (2026-10-24).** The static ARMv7 `naut` (the same
build as for the PLCnext) ran the same project on the processor's own
Linux, polling its own Modbus server: 1 Hz heartbeat on coil 587 as
read from the laptop, 77 tags, 4 % of one core, 17 MB resident, with
the PAC Control engine, groov View and Node-RED still running beside it. The
controller and its I/O now share a chassis; nothing is off-box.

That run surfaced a runtime bug worth more than the demo: the eleven
unwired ICTD channels read **NaN**, Go's JSON encoder refuses a document
holding one, and both `/api/state` (a 200 with an empty body) and the
SSE stream (every frame skipped) went silent — a blank dashboard with no
error anywhere, on any driver that can deliver a NaN. Fixed on this
branch at three layers: `runtime` renders a non-finite REAL as JSON
`null`; the `modbus` driver marks such a tag `bad` (the reading, not the
link); `/api/state` answers 500 with the encoder's reason if anything
else ever fails to render. Tests pin all three.


**On the OptoMMP driver (2026-10-24, afternoon).** The same chassis,
re-imported onto `driver: {type: optommp}`, ran from the laptop and then
on the EPIC (the static ARMv7 build — which first had to stop computing
memory-map addresses in `int`, they overflow it on 32-bit). DO11 read off
the processor over a second OptoMMP connection carried the 1 Hz
heartbeat; scratchpad float 0 carried the ICTD reading `bench.st`
publishes; the 11 unwired ICTD channels were Bad; zero driver errors.
Measured on the EPIC, one minute each, CPU and RSS of `naut` from
`/proc`, scan time from the driver's status row:

| driver + task scan | CPU (one core) | RSS | scan p50 / max | requests / scan |
|---|---|---|---|---|
| 100 ms | 3.9 % | 19.2 MB | 1.77 / 2.94 ms | 3 |
| 20 ms | 14.7 % | 20.9 MB | 1.70 / 3.42 ms | 3 |
| 5 ms | 35.3 % | 21.3 MB | 2.20 / 5.20 ms | 3 |

At 100 ms that is the Modbus path's cost with per-channel quality added;
5 ms is the floor (one scan in that minute took 5.2 ms). The bench has
four field wires from outputs to inputs (DO3→DI6, DO4→DI8, DO5→DI10,
DO6→DI12); the env-gated live test (`NAUT_OPTOMMP_LIVE=<host>
NAUT_OPTOMMP_LOOPBACK="1.3>0.6" go test ./optommp -run Live`) drives one
and reads it back in about one scan (21 ms at 20 ms), catches and clears
the on-latch, and writes and restores a scratchpad float. With
`epic-bench` running, forcing `EPIC_M1_DO03` on (`POST /api/forces`)
lights the chain within half a second: DO3 → wire → DI6 → `bench.st`
→ DO6 → wire → DI12, and clearing the force drops all four (re-checked
2026-10-25).

**Custom area and streaming on the EPIC (2026-10-24, evening).** The
live test passes in every mode (`NAUT_OPTOMMP_MODE=custom` or
`stream=20ms`) and checks the pointers and streaming configuration come
back as found. The bench project on the EPIC, one minute per row, with
mmpServer's own CPU beside `naut`'s (mmpServer idles at 6.6 %):

| inputs | naut CPU | naut RSS | mmpServer CPU | per scan |
|---|---|---|---|---|
| packed poll, 20 ms | 13.7 % | 20.7 MB | 12.4 % | 3 requests, 1.62 ms p50 |
| custom area, 20 ms | 12.0 % | 19.7 MB | 8.4 % | 1 request, 0.53 ms p50 |
| stream, 20 ms | 10.0 % | 19.5 MB | 8.3 % | none; longest gap 25.7 ms |
| stream, 5 ms (task 5 ms) | 20.5 % | 21.5 MB | 11.1 % | none; longest gap 24.1 ms, never late |

Streaming every 5 ms costs less than polling every 20 ms did, and a
quarter of what the 5 ms poll cost `naut` (35.3 %). From the laptop the
20 ms stream arrived p50 20.00 / p99 20.21 / max 20.28 ms apart. The
watchdog is tested against the in-process device only; it has not been
switched on on the bench unit (it moves real outputs; James's call).

**PREEMPT_RT on the EPIC (2026-10-24, night).** The processor's kernel is
`5.15.91-rt57 PREEMPT_RT`, and a task's `priority:` uses it once the
binary may: `sudo setcap cap_sys_nice+ep naut` (the shell user's rtprio
limit is 0). The bench project, polling, 20 ms task and 20 ms driver
scan, firmware 4.1.2.0, the PAC engine, groov View and Node-RED running
beside it. Each row is 5 minutes (≈ 15,100 scans) after a 10 s warm-up. Lateness
is the task's wake-up lateness from the runtime's own histogram, so values
are bucket ranges. "4 hogs" is four `while :; do :; done` shells at
normal priority, one per core. Priority 20 sits below mmpServer, which runs
16 threads at SCHED_FIFO 30.

| task | load | p50 | p99 | p99.9 | max | late > 2 ms | driver scan p50 / p99 / max |
|---|---|---|---|---|---|---|---|
| normal | quiet | 50–100 µs | 0.5–1 ms | 2–5 ms | 10–20 ms | 31 | 1.93 / 4.45 / 5.64 ms |
| FIFO 20 | quiet | 50–100 µs | 200–500 µs | 200–500 µs | 2–5 ms | 1 | 1.64 / 3.68 / 4.67 ms |
| normal | 4 hogs | 50–100 µs | 0.5–1 ms | 1–2 ms | 5–10 ms | 6 | 1.95 / 6.05 / 8.54 ms |
| FIFO 20 | 4 hogs | 50–100 µs | 100–200 µs | 200–500 µs | 200–500 µs | 0 | 1.79 / 5.88 / 6.10 ms |

FIFO 20 takes the tail off the task. Under load, its worst wake-up went
from 5–10 ms to under 0.5 ms, with none late. The quiet EPIC was worse than
the loaded one at normal priority. A likely cause is idle cores waking up,
but that was not investigated, nor was the one
2–5 ms wake left at FIFO 20 on a quiet EPIC. The driver gains nothing. Its
poll loop still runs at normal priority, and each request waits on
mmpServer, so the I/O scan's p99 is 4–6 ms in every row. The task's
timing is now tighter than the inputs it reads. Every row had zero
driver errors and no overruns.

**The driver at real-time priority (2026-10-25).** A manifest
`priority: N` runs the optommp driver's whole loop on one locked thread
at SCHED_FIFO N:
- the TCP socket is read and written with blocking syscalls on that
  thread;
- scans sleep to their slot with `clock_nanosleep`;
- a stream is received with blocking `recvfrom` on the same thread;
- commands are picked up between scans, a millisecond at a time.

This keeps Go's network poller and timers, which run on normal-priority
threads, out of the wake-up path. Same conditions as above, 5 minutes per
row, with the task at FIFO 20 where marked. The driver sits at 25, above
the task that consumes its inputs and below mmpServer. The driver's
numbers are its own: wake-up lateness and scan time over every scan since
start, so the first 10 s are included.

| task | driver | load | task p99 / max | late > 2 ms | driver wake p99 / max | driver scan p99 / max | naut CPU |
|---|---|---|---|---|---|---|---|
| normal | normal | quiet | 200–500 µs / 5–10 ms | 3 | 1–2 / 9.96 ms | 2–5 / 10.3 ms | 14.5 % |
| FIFO 20 | normal | quiet | 100–200 µs / 200–500 µs | 0 | 1–2 / 16.2 ms | 2–5 / 13.9 ms | 13.1 % |
| FIFO 20 | FIFO 25 | quiet | 100–200 µs / 200–500 µs | 0 | 0.1–0.2 / 5.54 ms | 2–5 / 24.8 ms | 16.5 % |
| normal | normal | 4 hogs | 200–500 µs / 2–5 ms | 8 | 1–2 / 6.44 ms | 2–5 / 14.6 ms | 13.4 % |
| FIFO 20 | normal | 4 hogs | 100–200 µs / 0.5–1 ms | 0 | 1–2 / 5.10 ms | 2–5 / 13.0 ms | 14.7 % |
| FIFO 20 | FIFO 25 | 4 hogs | 100–200 µs / 0.5–1 ms | 0 | 0.1–0.2 / 1.75 ms | 2–5 / 7.43 ms | 18.3 % |

The driver now starts each scan on time: wake-up p99 went from 1–2 ms to
under 0.2 ms, and under load the worst went from 5–6 ms to 1.75 ms. How
long a scan takes did not change. Its p99 is in the 2–5 ms bucket in every
row, because the time is spent inside mmpServer waiting for the reply.
The worst scan, 24.8 ms on a quiet EPIC with the driver at FIFO 25, was
not investigated. So for input freshness the gain is the wake-up's
1–2 ms at p99, and the cost is 2–4 points of `naut` CPU from the
millisecond command check. The bench project does not set either
priority. The 1-2-5 buckets are too coarse to separate scan times inside
2–5 ms; the runtime's finer histogram would.

### 2.4 Not done, and why

- **groov Manage REST API.** Richer (module names, channel names from the
  configuration), needs an API key James would mint. OptoMMP needed none,
  so it was not pursued.
- **Channel quality on the Modbus path.** It still carries none beyond
  the NaN verdict; the quality mask, codes and present mask are OptoMMP
  only. Done in the optommp driver (§2.2).
- **A path to the I/O below OptoMMP: there is none, Opto's CODESYS
  driver included.** Read from the free *Opto 22 Library Package for
  CODESYS* 3.1.3.0 (`Opto22-3.1.3.0-codesys-b2.signed.package`, 2026-03;
  nothing installed on the EPIC). The EPIC PLC device description
  (`devices/plc/grv-epic-pr1.devdesc.xml`, runtime 3.5.21.40) hangs its
  I/O off the library FB `IoDrvGroovPLC`, and the I/O unit
  (`devices/iounit/grv-epic-pr1.devdesc.xml`) off `IoDrvOptoIoUnit`,
  whose parameters are *Address* (default `'127.0.0.1'`, "Use
  '127.0.0.1' for local I/O") and *Port* (default `2001`, "OptoMMP
  Port"). No native component, `.so`, device node or shared-memory name
  appears in the device descriptions or the compiled library
  (`Opto22.compiled-library-v3`). The driver is plain IEC code over the
  runtime's `SysSocket`: a TCP command/response client (`MmpClient`) and
  UDP sockets (`SysSockCreateUdp`, `SysSockSendToUdp`/`RecvFromUdp`), with
  OptoMMP transaction codes and labels checked on every reply. So Opto's
  own CODESYS runtime reaches local I/O exactly as `naut` does, as an
  OptoMMP client of `mmpServer` on loopback, and OptoMMP is the floor for
  everyone. Three OptoMMP features it leans on are worth taking for the
  `optommp` driver (branch `optommp-driver`). (1) **Streaming**: the
  driver writes the stream target address, port, start address, data
  size and interval (*Streaming Interval*, default 20 ms, "equal to or
  shorter than the bus-cycle time"), and the EPIC then pushes inputs as
  UDP datagrams into one shared socket, instead of being polled.
  (2) The **custom data area** ("MMP Pointers"): scattered channel
  addresses are mapped into one contiguous block, sized for "4 pointers
  × 32 channels × 16 modules = 2048", so a whole rack is one read or one
  stream. Where the pointers point matters, and the strings do not show
  which addresses Opto's driver uses. Measured on the `optommp-driver`
  branch: a pointer into the packed values (`F1000300`) costs `mmpServer`
  about 0.5 ms each time it is resolved. 36 of them streamed every 20 ms
  held it at 90 % of a core. Pointers into the expanded per-channel read
  areas return the same values, NaN included, for next to nothing. The
  stream configuration is also stored to flash, so a driver that sets it
  must restore it on a clean stop. (3) The I/O unit's **communication watchdog** (off by default)
  and a **power-up-clear check** ("PUC expected before communicating
  with I/O unit"). On quality: the CODESYS process image carries **no
  per-channel status**. An analog channel maps `EU` (a REAL) and nothing
  else; a digital channel maps state, on/off latch and feature value.
  Status is per module and per link: a diagnostic string, *Missing
  module*, *Invalid channel type… may not be compatible* at
  configuration time, timeouts named "no response to command" and
  "missed incoming stream", the watchdog timeout and auto-reconnect.
  That is a limit of Opto's CODESYS mapping, not of OptoMMP: the memory
  map also has a per-channel quality mask in the expanded digital packed
  data (`F1001900`) and quality indicator codes (`F1002080`), which the
  `optommp` driver reads and CODESYS does not surface.

  **What sits under `mmpServer`** (read-only look on the bench EPIC,
  2026-10-24; nothing changed). The groov I/O modules are not reached
  through an FPGA device node. The backplane is **ARCNET**: a COM20020
  controller brought up as `arc0` (node 01, about 470 frames received a
  second), driven by Opto kernel modules `com20020-o22`, `arc-o22proto`
  (which registers a socket family, `O22ArcProto`) and `arc-o22encap`.
  `/dev/fpgaDirectIo` (`root:mmp22 0660`) belongs to `o22_civ_direct_io`,
  "a basic char driver to toggle direct I/O on fpga", and is not the
  module I/O path. The kernel modules are all GPL, but no source or
  written offer is published or on the box, so it would have to be
  requested from Opto, and it would show only the ARCNET transport. The
  module protocol itself (commands, channel configuration, framing) is
  in `mmpServer`, which is closed. Replicating it is not worth doing.
  `mmpServer` is the bus's one processor node, so a second master beside
  it would interleave transactions with the modules. Replacing it means
  stopping `mmpServer`, which takes OptoMMP, Modbus, PAC Control, CODESYS
  and groov Manage's I/O with it, and it needs root. The gain would be
  small: `mmpServer` almost certainly scans the bus itself and answers
  OptoMMP from its own image. OptoMMP's UDP streaming is the
  low-latency path, and it is the one Opto's own runtime uses.
- **Outputs through pointers.** Opto's CODESYS driver also maps outputs
  into the custom area and writes them all each cycle. The driver writes
  on change instead — every digital output in one must-on/must-off write
  — so there is nothing to gain and the "only commanded channels"
  guarantee to lose.
- **Sharing the custom area or the stream.** The processor has one
  stream configuration and one set of pointers; the driver owns pointers
  0..n-1 and stream target #1 while it runs, warns when it finds them in
  use, and notices within 5 s if another client rewrites the pointers.
  Two nautilus instances (or nautilus and CODESYS) on one unit would
  fight; a manifest offset into the pointer table is the fix if it ever
  matters.
- **Watchdog values per channel.** The manifest sets the unit's watchdog
  time; which outputs it moves, and to what, stays groov Manage
  configuration (the driver reports how many bound outputs have one).
  Not switched on on the bench yet (§2.3).
- **The scan time itself.** Driver `priority:` (§2.3) makes the driver
  start on time; the 2–5 ms a scan takes is inside mmpServer and not
  ours to shorten. Going above mmpServer's 30 would starve the server
  every request waits on. Streaming removes the request entirely.
- **Counter reset.** `_Count` reads the feature value; clearing it (a
  read of the read-and-clear area's +0 quadlet) is not exposed yet.
- **Analog min/max** (`F0260008`) is not bound; one read per module per
  scan for a value groov Manage already shows.
- `naut optommp import` names tags by slot and channel; channel names
  configured in groov Manage are not reachable over OptoMMP.
- **Start on boot.** The on-EPIC runs were hand-started over SSH; a systemd
  unit for the shell user (systemd is there) is the obvious next step,
  with a `naut build` of the project so one binary is the deliverable.
- `plcnext` reports one quality for the whole image; a NaN in a float32
  binding there renders null but is not marked bad yet.

## 3. PLCnext — the nautilus runtime on the controller

### 3.1 What the bench unit is

`192.168.82.216`, `AXC F 2152`, firmware `23.0.0.65` (2023.0 LTS), Linux
5.4 PREEMPT_RT, ARMv7 dual-core, 490 MB RAM, 427 MB free on `/`. SSH as
`admin` works (the `tentacle-plcnext` credentials). Ports: 22, 80/443,
4840 (OPC UA), 41100 (Engineer), 44818 (EtherNet/IP). The gRPC server is
on the Unix socket `/run/plcnext/grpc.sock` only (50051 closed). The
firmware's `libArp.Services.Grpc.so` registers Data Access, Subscription,
Force, Device, Axioline master and acyclic, file, notification and
licence services; **`IAxioMasterService.AxioControl` and
`ReadConfiguration` answer `Unimplemented` on this firmware** (tried
live), so DDI-command bus configuration from outside is out.

Four Axioline devices are on the bus. The earlier `tentacle-plcnext`
experiment (2025-04) recorded them as device types `0x00400002` (16 bits
in — a DI16), `0x00800002` (16 bits out — a DO16), `0x00000100` (no
process data) and `0x00200008` (32 bits in and out — an AI2/AO2 shape)
and wrote per-device TIC files for them. **That configuration fails at
boot** (`Output.log`: `AXIO Master with 4 devices` → `CheckConfirmation():
AXIO conf with neg. result: cmd 0x0711, errCode 0x0c13, addInfo 0x0004` →
`Abort download`), so today no `Arp.Io.AxlC/…` port exists; every name
tried returns `DAE_NotExists`. The same log shows that on 2025-04-05 the
**BusConductor** component ran on this firmware and configured the bus
("Creating local I/O configuration… Done enabling data output"), and its
`libBusConductor.so` is still in the project's `Libs/` (md5 identical to
upstream) — it was later unplugged from `pcwe.plm.config` in favour of
the hand-written TICs. Restoring the stock BusConductor deployment was
the fix, done 2026-10-24 as BusConductor's own no-Engineer layout: a
separate `/opt/plcnext/projects/BusConductor` with the `current`
symlink pointed at it (PCWE untouched, rollback is the symlink), and a
runtime restart. The recipe and the three config files it needs are in
`examples/plcnext-bench/` (`controller/`).

**What the firmware's data-access service turned out to be** (measured
on 2023.0, each point by trying it; the brief's whole PLCnext path
rests on these three):

1. It resolves ports of MainProcess components only — eCLR variables,
   ESM, Device.Interface, PLM components such as BcComponent1. The I/O
   components' own ports (`Arp.Io.AxlC/0.DI4096`, even the standard
   `Arp.Io.PnC/PNIO_SYSTEM_BF`) answer `DAE_NotExists` under every
   spelling, and the OPC UA server with `GdsPortsToProvide=All` lists
   the same set (4 763 nodes, none under `Arp.Io.*`). I/O data reaches
   a service only through a **GDS connector** into a MainProcess port.
   Phoenix Contact's own runtime samples read I/O through the in-process
   C API for this reason.
2. It reads and writes **array elements by name** (`Port[17]`) and
   answers `DAE_NotSupported` for an array (or struct) as a whole.
3. BusConductor's request flags act on a **rising edge sampled by a
   100 ms task**: a flag raised and dropped inside one cycle is never
   seen. The first handshake "succeeded" (CONFIGURED, four modules) and
   left the bus configured but not running; `AXIO_DIAG_STATUS_REG_RUN`
   is the truth, and the driver now reads it with every scan.

The connector that makes the image reachable with no Engineer project
and no SDK: a bare controller's eCLR exposes the PROFINET-device
process-image system variables `Arp.Plc.Eclr/PND_S1_INPUTS` and
`PND_S1_OUTPUTS`, 512 bytes each — exactly the BusConductor image size —
and the project's `PCWE.gds.config` normally connects them to the
(unused) PROFINET device. Re-pointing those two connectors at
`Arp.Io.AxlC/0.DI4096` and `DO4096` mirrors the Axioline image onto
ports the service can read. A purpose-built C++ component with two
array ports is the tidy version of the same thing, and needs the SDK;
an empty Engineer program with two array ports is the other, and needs
Engineer once. Both are listed under not-adopted-yet, not never.

### 3.2 Architecture

```
  controller (AXC F 2152)
  ┌──────────────────────────────────────────────────────────────┐
  │  plcnext firmware                                            │
  │   Axioline master ──500 µs──▶ Arp.Io.AxlC/0.DI4096 ─┐ GDS    │
  │                     ◀──────── Arp.Io.AxlC/0.DO4096 ◀┤connector│
  │                 Arp.Plc.Eclr/PND_S1_INPUTS  ◀───────┘ (gds.  │
  │                 Arp.Plc.Eclr/PND_S1_OUTPUTS ────────┘ config) │
  │   BusConductor (BcComponent1): CONFIG_REQ ⇡ → CONFIGURED,    │
  │                                START_IO_REQ ⇡, NUM_MODULES   │
  │   Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_RUN  (the bus is running)│
  │   IDataAccessService over unix:///run/plcnext/grpc.sock      │
  │                 ▲ Read [PND_S1_INPUTS[0..n), RUN]  one RPC   │
  │                 ▼ Write [PND_S1_OUTPUTS[0..m)]     one RPC   │
  │  naut (one static binary, the project on its tail)           │
  │   plcnext.Driver: handshake (idempotent, held edges) · poll  │
  │     the input span + RUN at scan-rate · flush the output     │
  │     span when any output changed · RUN=false ⇒ inputs Bad    │
  │   runtime scan loop · tag API :8080 · HMI                    │
  └──────────────────────────────────────────────────────────────┘
```

```
plcnext/
  proto/, gen/   Phoenix Contact's Data Access protos (from github.com/PLCnext/gRPC), buf-generated
  gds.go         Client over grpc: element-wise ReadBytes/WriteBytes (Read/Write multi-item RPCs),
                 scalars, Describe and WriteScalar for the CLI
  manifest.go    plcnext_manifest.yaml: socket, bus-conductor, inputs/outputs (the mirrored ports),
                 run-port, byte-order, modules (documentation), tags {name, dir, offset, format
                 bit:N|int16|…, scale, bias}; span() = how many bytes the bindings reach
  codec.go       bindings ⇄ the byte image; bit writes merge into the kept output image
  driver.go      io.Driver + BatchReader + QualityReporter; New never dials; Start: dial →
                 conductBus (skip what is already configured/running; edge = low, 300 ms, high) →
                 poll loop; reconnect with backoff; Health for /api/drivers; Handshake for the CLI
  tags.go        tag file from the manifest
cmd/naut/plcnext.go   naut plcnext browse | read | write (on the controller) | tags
internal/project      driver type "plcnext"; status row (connected / bus not running / …); schema enum
examples/plcnext-bench/controller/   the three project files that differ from upstream BusConductor:
                 PCWE.gds.config (the two connectors), pcwe.plm.config (library path), PCWE.opcua.config
```

Dependencies: `google.golang.org/grpc` (new) and `protobuf` (already
present), inside `plcnext/` only; the core stays stdlib. The ARMv7 static
build with both new packages is 22 MB (20 MB before them).

### 3.3 Verified, and not

Verified on the controller (2026-10-24): BusConductor configures the bus
(`CONFIGURED=true NUM_MODULES=4`), the held-edge `START_IO_REQ` brings
it to `RUN=true` (`ACT`, `RDY` true; `PF` true because slot 4 has no UP
supply — see below), the GDS connectors deliver live data into the
mirrored ports, and `naut run` of `examples/plcnext-bench` on the
controller drove the DO16 module's output 15 at 1 Hz as read back from
the output mirror, with the status row saying "Axioline bus running,
4 module(s)". Cost on the dual-core ARM: 9 % of one core, 23 MB
resident, beside a firmware that already idles at load 4–5. The gRPC
round trip for 8 elements plus the RUN bit is ~11 ms, so the 50 ms
scan is honest; the scan itself executes in under 1 ms.

Measured, not assumed: in BusConductor's generic mode every module
occupies its frame length in BOTH images (DI16 2 bytes, DO16 2 bytes,
the 0-bit device 0, AI2/AO2 4 bytes), so the analog inputs are at
input bytes 4..7 and the DO16 at output bytes 2..3, not the 2..5 / 0..1
the hand-typed manifest assumed. The manifest now says so.

`go test ./plcnext/` runs the driver against an in-process
`IDataAccessService` that now behaves like the real one on the three
points in §3.1 (whole-array reads refused, elements served, RUN raised
by START_IO_REQ, idempotent reconnect), plus bit merges, a bus that
stops (inputs Bad) and restarts (Good), and `DAE_NotExists` on a
missing connector → `NotConnected`. `examples/plcnext-bench/` checks
and tests offline. Cross-compile is clean.

Not verified: a human looking at the DO16 LEDs (the output mirror
toggles; the module's UP supply is what makes a channel actually
switch, and the bus reports "Supply missing (Up)" on slot 4, so James's
eyes on slot 2 are the next data point), analog values (slot 4 reads
0x8020, Axioline's supply-missing marker, until UP is wired), and a
reboot of the controller (the symlink and project files persist; the
driver re-handshakes on its own).

### 3.4 The tiers

1. **Best-effort (built):** gRPC data access at a scan rate of tens of
   milliseconds. Remote-I/O class. Honest about it in the status row.
2. **In-process (stage 1 built and running on the bench, 2026-10-24):**
   `rt/plcnext-io/nautilus_io`, an ACF-started C process that joins the
   firmware as a module application and reads/writes the Axioline frames
   through the ANSI-C API in its own 1 ms cycle — Phoenix Contact's
   `SampleRuntime` shape, built **without the SDK** (every entry point is
   a C export of a shipped library, `ArpSystemModuleMain2` standing in
   for the static `ArpSystemModule_Setup`; zig cross-compiles it). Its
   input frame matches the gRPC mirror byte for byte; it is the output
   frame's only writer; ~2 % of a core. Best-effort scheduling for now
   (file capabilities are needed for SCHED_FIFO and `admin` cannot
   `setcap`): about 1 cycle in 1 500 later than 200 µs, worst 1.7 ms.
   Three hard lessons are in its README: never exit during ACF start-up
   (it rebooted the controller), never call the ANSI-C surface before
   the PLC's first `Start*` callback, never guess a signature. Stage 2
   is the seqlock page `rt/fastloop` defines, between this process and
   the Go driver (`transport: shm`), so the manifest and tags stay as
   they are and only the bytes' route changes.
3. **Not pursued:** the OPC UA server on 4840 as the I/O path — it sees
   exactly what data access sees (the mirrored eCLR ports, never raw
   Axioline ports) at a higher cost per read, though it was the tool
   that proved the point (`naut opcua browse` from the opcua branch, with
   `GdsPortsToProvide=All`); and the EtherNet/IP adapter on 44818
   (would need an Engineer project to map).

## 4. What each path is worth

- **EPIC today:** a credible "nautilus controls real I/O" demo on a
  recognisable product, with nothing installed on the product. The import
  story (`browse`, `import`, commit, `check`) is the same one every other
  driver tells. Cost: one driver package (stdlib only), no runtime change.
- **PLCnext, running since 2026-10-24:** the first instance of *nautilus as the
  runtime on a vendor's own PLC hardware* — the N-55 / N-90 idea, and the
  thing no facade can claim. Cost so far: one driver package and a gRPC
  dependency. Cost to make it deterministic: the `rt/` shim.
- **Both as examples:** `examples/epic-bench` and `examples/plcnext-bench`
  are the office bench, like `examples/it-rack`; they are not plants and
  are not listed with the four.

### Next steps, in order

1. ~~James: restart the PLCnext runtime after the BusConductor restore,
   then on the controller `naut-arm plcnext browse` and the heartbeat
   run.~~ Done 2026-10-24 (§3.3). Next on that side: James looks at the
   DO16 LEDs while the heartbeat runs, wires UP to slot 4 so the analog
   module reads, and the on-controller run gets a systemd unit.
2. ~~James: order the free `GROOV-LIC-SHELL`; then the EPIC run moves onto
   the EPIC.~~ Done 2026-10-24: the license was already on the unit, the
   shell is on, nautilus ran on the EPIC. Next on that side: a systemd
   unit so it survives a reboot.
3. Record both for N-90 (the three-controllers piece) while they are live.
4. `naut plcnext import` from a bus scan (module list → manifest) once
   BusConductor's `NUM_MODULES` and the image are readable, so the
   offsets stop being hand-typed.
5. Decide whether tier 2 is wanted before anyone builds it.

## 5. Not adopted

- ~~An OptoMMP `io.Driver`: the Modbus driver already does the job; one
  transport per device class.~~ Reversed 2026-10-24 (§2.2): Modbus on the
  EPIC is the same server, no faster, and blind to channel quality.
- DDI bus configuration over `IAxioMasterService`: unimplemented on this
  firmware; BusConductor is the vendor's supported answer.
- Driving the EPIC through groov Manage's REST API or the PLCnext through
  OPC UA: both need credentials or an Engineer project the protocols
  below them do not.
- Committing on James's behalf: the branch is left uncommitted in the
  worktree for review.
