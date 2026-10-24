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
the hand-written TICs. Restoring the stock BusConductor deployment is the
fix; it is a file copy plus `sudo /etc/init.d/plcnext restart`, which
this session's sandbox refused as a remote write. The exact recipe is in
`examples/plcnext-bench/README.md`.

### 3.2 Architecture

```
  controller (AXC F 2152)
  ┌──────────────────────────────────────────────────────────────┐
  │  plcnext firmware                                            │
  │   Axioline master ──500 µs──▶ GDS  Arp.Io.AxlC/0.DI4096      │
  │                     ◀────────       Arp.Io.AxlC/0.DO4096     │
  │   BusConductor (BcComponent1): CONFIG_REQ → CONFIGURED,      │
  │                                START_IO_REQ, NUM_MODULES     │
  │   IDataAccessService over unix:///run/plcnext/grpc.sock      │
  │                 ▲ ReadSingle / WriteSingle                    │
  │  naut-arm (one static binary, the project on its tail)       │
  │   plcnext.Driver: handshake · poll DI4096 at scan-rate ·     │
  │                   write DO4096 whole when any output changed │
  │   runtime scan loop · tag API :8080 · HMI                    │
  └──────────────────────────────────────────────────────────────┘
```

```
plcnext/
  proto/, gen/   Phoenix Contact's Data Access protos (from github.com/PLCnext/gRPC), buf-generated
  gds.go         Client over grpc; OctetstringN ⇄ []byte (one Uint8 ObjectType per byte)
  manifest.go    plcnext_manifest.yaml: socket, bus-conductor, inputs/outputs, byte-order,
                 modules (documentation), tags {name, dir, offset, format bit:N|int16|…, scale, bias}
  codec.go       bindings ⇄ the byte image; bit writes merge into the kept output image
  driver.go      io.Driver + BatchReader + QualityReporter; New never dials; Start: dial →
                 BusConductor pulse → size the output image from the controller → poll loop;
                 reconnect with backoff; Health for /api/drivers
  tags.go        tag file from the manifest
cmd/naut/plcnext.go   naut plcnext browse (on the controller) | tags
internal/project      driver type "plcnext"; status row; schema enum
```

Dependencies: `google.golang.org/grpc` (new) and `protobuf` (already
present), inside `plcnext/` only; the core stays stdlib. The ARMv7 static
build with both new packages is 22 MB (20 MB before them).

### 3.3 Verified, and not

Verified: `go test ./plcnext/` runs the driver against an in-process
`IDataAccessService` that behaves like the firmware with BusConductor
behind it (handshake order, CONFIGURED latch, 512-byte images, bit
merges, `DAE_NotExists` on an unconfigured bus → `NotConnected` quality
and a status-row error). `examples/plcnext-bench/` checks and tests
offline. Cross-compile is clean.

Not verified: anything on the controller after the restart — the port
names BusConductor creates on *this* firmware, the byte order of Axioline
words in the image (manifest default `big`; one bit on one LED settles
it), analog scaling, and the real poll latency. `naut plcnext browse` is
written for exactly that first session.

### 3.4 The tiers

1. **Best-effort (built):** gRPC data access at a scan rate of tens of
   milliseconds. Remote-I/O class. Honest about it in the status row.
2. **Deterministic (next):** an Arp C-API program (`ArpPlcIo_GetBufferPtrByPortName`,
   `ArpPlcGds_BeginRead/Write`) running in the ESM cycle, as Phoenix
   Contact's `SampleRuntime` (C++) and `rust-sample-runtime` do. Needs the
   PLCnext SDK matching firmware 2023.0 and cgo across the armhf Yocto
   sysroot, which is why `rt/` (the Rust fast loop + shared-memory tag
   exchange) is the natural home: a small C/Rust I/O shim exchanging the
   image with the Go runtime over shared memory, not cgo in the core.
3. **Not pursued:** the OPC UA server on 4840 as the I/O path (it carries
   the eCLR program's variables, not raw Axioline ports) and the
   EtherNet/IP adapter on 44818 (would need an Engineer project to map).

## 4. What each path is worth

- **EPIC today:** a credible "nautilus controls real I/O" demo on a
  recognisable product, with nothing installed on the product. The import
  story (`browse`, `import`, commit, `check`) is the same one every other
  driver tells. Cost: one importer package, no runtime change.
- **PLCnext after the restart:** the first instance of *nautilus as the
  runtime on a vendor's own PLC hardware* — the N-55 / N-90 idea, and the
  thing no facade can claim. Cost so far: one driver package and a gRPC
  dependency. Cost to make it deterministic: the `rt/` shim.
- **Both as examples:** `examples/epic-bench` and `examples/plcnext-bench`
  are the office bench, like `examples/it-rack`; they are not plants and
  are not listed with the four.

### Next steps, in order

1. James: restart the PLCnext runtime after the BusConductor restore
   (recipe in `examples/plcnext-bench/README.md`), then on the controller
   `naut-arm plcnext browse` and the heartbeat run.
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
