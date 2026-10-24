# Design brief: nautilus on open controllers — groov EPIC and PLCnext

Status: **groov EPIC built and verified on the bench; PLCnext built and
verified against a fake firmware, one restart away from the bench.**
Written 2026-10-23 on the `plcnext-groov` branch (worktree
`~/Development/joyautomation/nautilus-plcnext-groov`). Context: this is
**Field drivers** work (`optommp/` is an importer; `plcnext/` is a driver)
plus two bench projects; nothing above the `io.Driver` seam changed.

**The question asked:** two controllers are on the office network, an
Opto 22 groov EPIC and a Phoenix Contact PLCnext. Can nautilus use them as
a *target* for running IEC 61131-3 — through the nautilus runtime, or
through theirs if it is open?

**The short answer:** yes, through the nautilus runtime, differently on
each. Neither vendor's own IEC engine is usable as a target (§1). The EPIC
is nautilus I/O today with zero new runtime code: the processor's stock
firmware exposes the chassis over OptoMMP and Modbus/TCP, a new importer
turns the chassis into a Modbus manifest, and a 1 Hz heartbeat written by
an ST program was read back off the EPIC's own coil (§2). The PLCnext is
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
Manage), **502 Modbus/TCP**, **2001 OptoMMP**, 8088 (Ignition Edge, Jetty),
22001 (PAC Control engine). 22 was closed on the first day; the shell
license was already on the unit and James enabled the shell server and a
shell user on 2026-10-24. From the shell: quad-core ARMv7 (Cortex-A9
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

**Decision: OptoMMP for discovery, Modbus for scan.** The runtime already
has a Modbus driver with block planning, quality, status rows, a bench
slave and CI. A second scan-rate transport for the same bytes would be a
second driver to keep honest for no gain; the chassis knowledge (module
tables, type codes, address arithmetic) is exactly what an *importer*
should hold, behind the anti-corruption rule that vendor spellings stop at
the package. Hence:

```
optommp/
  wire.go       read/write block over TCP 2001 (pure stdlib)
  modules.go    form 1465's groov I/O table as data: module type → part + channel count,
                channel type → range/unit; Kind from the family byte (0x50/0x54 DI, 0x90/0x94 DO, 0x60 AI, 0xA0 AO)
  chassis.go    Scan: one read per slot + the three packed areas → Chassis{Modules{Channels}}
  modbusmap.go  Chassis → modbus.Manifest + tag file on the form 1678 addresses
cmd/naut/optommp.go   naut optommp browse | import
```

`naut optommp import --host <epic>` writes `modbus_manifest.yaml` and
`tags/optommp.yaml`; the project says `driver: {type: modbus}`. Tag names
are `<id>_M<slot>_<Kind><ch>` — `EPIC_M1_DO11`. Outputs are writable
bindings (coils, holding floats).

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
Ignition Edge and the PAC Control engine still running beside it. The
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

### 2.4 Not done, and why

- **groov Manage REST API.** Richer (module names, channel names from the
  configuration), needs an API key James would mint. OptoMMP needed none,
  so it was not pursued.
- **Channel quality from the present mask.** The Modbus path carries no
  per-channel quality beyond the NaN verdict above; the channel-present
  mask is only on OptoMMP. An OptoMMP *driver* could carry it; not worth
  it until a project needs it.
- `naut optommp import` names tags by slot and channel; channel names
  configured in groov Manage are not reachable over OptoMMP.
- **Start on boot.** The on-EPIC run was hand-started over SSH; a systemd
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
  driver tells. Cost: one importer package, no runtime change.
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

- An OptoMMP `io.Driver`: the Modbus driver already does the job; one
  transport per device class.
- DDI bus configuration over `IAxioMasterService`: unimplemented on this
  firmware; BusConductor is the vendor's supported answer.
- Driving the EPIC through groov Manage's REST API or the PLCnext through
  OPC UA: both need credentials or an Engineer project the protocols
  below them do not.
- Committing on James's behalf: the branch is left uncommitted in the
  worktree for review.
