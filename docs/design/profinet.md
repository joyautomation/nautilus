# Design brief: PROFINET IO controller and IO device for nautilus

Status: **proposal, 2026-11-17** (#723, part of #725). Nothing is built
yet. Context: **field drivers** (anti-corruption layer; context map §1).

**Goal:** PROFINET RT in both directions, behind `io.Driver`.

- **`type: profinet-controller`.** nautilus is the IO controller. It
  drives PROFINET devices: drives, remote I/O, and IO-Link masters
  presented as PROFINET devices.
- **`type: profinet-device`.** nautilus is an IO device to a PLC. The
  layout is set in the manifest, and `naut profinet gsdml` writes the
  GSDML the PLC's engineering tool imports.

Both drivers follow the rules the Modbus (`docs/design/modbus.md`),
EtherNet/IP and OptoMMP (`docs/design/open-controllers.md`) drivers
follow for health, quality, the write gate and redundancy.

**Done when:**
- the bench PLC exchanges cyclic data with nautilus as its device, and
- nautilus as controller runs a PROFINET device.

## 1. Where it comes from, and what it lacks

The stack is ported from tentacle-next's pure-Go implementation, which
lives in two packages there.

`internal/profinet` (the device) is about 9k lines. Its parts:
- DCP and its responder
- LLDP
- the AR over DCE/RPC: Connect, Write, PrmEnd, ApplicationReady, Release
- cyclic RT_CLASS_1 frames at the controller's FrameOffsets
- RTA acknowledgements
- GSDML generation

Its git history is a long series of fixes made against TIA Portal, and
that history is most of its value. The protocol code has a clean seam
(`DeviceCallbacks`). The rest is tentacle glue (`manager.go`, `layout.go`,
`profinet.go`: NATS, KV, a netplan apply path), and it is replaced, not
ported.

`internal/profinetcontroller` is **not portable as it stands**. Its
Connect has these gaps:
- no APIs, IODataObjects or FrameOffsets;
- the output frame carries one IOCS byte, and DataStatus never says Run;
- no parameter records are written;
- the device's ApplicationReady is never answered;
- no DCP Set, and no GSDML parsing.

The source's own device would emit no outputs to it. The controller is
therefore **rebuilt here on the shared wire code**. `pnio_blocks.go`,
`rpc.go` and `dcp.go` carry most of the encoding for both directions.

Some behaviour that nautilus needs is missing on both sides. Each item is
a rule in §4:
- The device never acts on its watchdog: `WatchdogExpired` has no caller,
  so a silent PLC leaves outputs frozen and the AR "connected".
- The device never reads the controller's DataStatus (Run/Stop) or IOPS.
  A PLC in STOP still delivers "good" outputs.
- The device accepts any expected configuration without a ModuleDiff.
- DCP Set applies IP settings to the host when enabled, and is
  unauthenticated.
- No alarms are raised.

## 2. Package layout

```
profinet/
  wire/        pure: DCP, DCE/RPC header + PNIO body, PNIO blocks, RT frame,
               RTA, LLDP TLVs, I&M records, value pack/unpack. No I/O.
  transport/   Transport interface; Hub (in-memory, tests); AF_PACKET on
               Linux (transport_linux.go), "not supported" elsewhere
  gsdml/       GSDML from a device manifest (pure)
  device/      the IO device: DCP responder, LLDP sender, RPC server, AR
               state machine, cyclic provider/consumer
  controller/  the IO controller: DCP client, RPC client, AR sequence,
               cyclic provider/consumer
  manifest.go  both manifests (one file, two shapes)
  driver_controller.go, driver_device.go, status.go   io.Driver + Health
  codegen/     later: `naut profinet import` (GSDML → controller manifest)
```

**The protocol's names stop at this package** (context map §2.2): AR,
IOCR, IOPS, FrameID and station name do not appear above `io.Driver`.
What crosses the seam:
- `io.Values` with `lang/ir`-compatible scalars;
- `io.Quality`;
- `internal/tagfile.Tag` rows from the generator.

**Dependency.** The source uses `mdlayher/packet` for AF_PACKET.
nautilus's core is pure stdlib plus a few vetted dependencies, and the
stdlib `syscall` package is enough (built, #728). It gives an AF_PACKET
socket with a classic BPF EtherType filter and `PACKET_ADD_MEMBERSHIP`.
That last option joins the DCP multicast group (01:0E:CF:00:00:00) instead
of putting the NIC in promiscuous mode. The source added promiscuous mode
because its library did not expose the membership call. Promiscuous mode
stays only as a fallback if membership proves insufficient on a macvlan or
bridge child, and that would be measured on the bench.

## 3. Manifests

The format is hyphenated keys with `KnownFields(true)`, as in
`optommp_manifest.yaml`. Ident numbers are hex.

**Wire formats** (`format:`) are big-endian, as PROFINET is:
- `bool`, which needs `bit:`
- `int8`, `uint8`, `int16`, `uint16`, `int32`, `uint32`, `int64`,
  `uint64`
- `float32`, `float64`

The tag file carries the IEC type: BOOL, SINT, USINT, INT, UINT, DINT,
UDINT, LINT, ULINT, REAL, LREAL. Strings and arrays are out of scope in
this wave.

### 3.1 Controller

```yaml
# profinet_manifest.yaml (type: profinet-controller)
interface: eth1                # the NIC the PROFINET network is on
devices:
  - id: rio1                   # the device row on /api/drivers; quality scope
    station: rio-1             # station name, resolved by DCP Identify
    # mac: / ip: optional pins; ip without mac still needs DCP (L2 has no ARP)
    set-name: false            # true: DCP Set Name onto the device at `mac:`
    vendor-id: 0x002A          # checked against DCP Identify; mismatch = parked
    device-id: 0x0313
    cycle: 8ms                 # SendClock × ReductionRatio; ≥ GSDML MinDeviceInterval
    watchdog-factor: 3         # device data-hold
    slots:
      - slot: 1
        module-ident: 0x00000031
        subslots:
          - {subslot: 1, submodule-ident: 0x00000001, inputs: 2, outputs: 2}
    records:                   # parameter records written before PrmEnd (from GSDML)
      - {slot: 1, subslot: 1, index: 0x0001, data: "0102"}
tags:
  - {name: RIO1_DI, device: rio1, slot: 1, subslot: 1, dir: input,  offset: 0, format: uint16}
  - {name: RIO1_DQ0, device: rio1, slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool}
```

### 3.2 Device

```yaml
# profinet_device.yaml (type: profinet-device)
interface: eth1
station: nautilus-1
vendor-id: 0x....              # open question §8
device-id: 0x....
min-cycle: 8ms                 # GSDML MinDeviceInterval
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 8, outputs: 4}
tags:
  - {name: Bin1_Level, slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}  # to the PLC
  - {name: Bin1_Fill,  slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool} # from the PLC
```

On the device, `dir` is seen from the **PLC**, because that is how the
GSDML and the engineering tool read it. On the nautilus side, a PLC
output is an input tag and a PLC input is an output tag; the generated
tag file says so. `naut profinet gsdml -m profinet_device.yaml` writes
`GSDML-V2.35-<vendor>-<station>-<date>.xml` from the same file the driver
loads, so the PLC's configuration and what the driver serves come from one
source.

`New` validates everything offline:
- overlaps within a subslot;
- sizes against the declared lengths;
- GSDML length limits (244 bytes per direction per IOCR in this wave);
- `bit:` only on `bool`.

It never opens a socket, so `naut check` and `naut build` pass on any
laptop.

## 4. Behaviour

### 4.1 Quality and online

Each controller device and the device's AR is one `io.Connection` with a
`<name>__Online` BOOL.

| Situation | Controller (tags of that device) | Device (input tags from the PLC) |
|---|---|---|
| never exchanged data | NotConnected | NotConnected |
| AR up, data valid | Good | Good |
| watchdog expired / AR aborted / released | Stale, last value held, `__Online` false | Stale, `__Online` false |
| peer's IOPS for that subslot Bad | Bad (that subslot only) | Bad (that subslot only) |
| peer's DataStatus says Stop / Problem | Bad for the whole device | Bad for every tag (PLC in STOP) |
| ModuleDiff: subslot wrong or missing | Bad for that subslot; diff in the driver row | n/a: the device answers with a ModuleDiff (§4.4) |

Outputs (nautilus → peer) are the program's commands; their quality is
not reported back.

### 4.2 The write gate

The cyclic sender runs outside the scan, every cycle. That makes it a
permanent keep-alive: the always-on form of the Modbus `rewrite:` case
(modbus.md §9.3, #625). The gate (`SetWriteGate`; `naut run` installs
leadership with `redundancy:`, otherwise the live-scan gate) decides what
the frame says, not whether frames flow.

- **Controller, gate closed:**
  - output DataStatus goes to Stop, and the IOPS of every output subslot
    goes to Bad;
  - frames keep flowing, so the AR stays up and the device applies its
    own substitute values;
  - a device without substitute values holds its last outputs, and the
    driver row says "gate closed: outputs invalid".
- **Controller, gate reopens or AR re-established:** forget every command
  and ask for every output (`io.OutputsRequester`). The first frame with
  Run is built from the program's values, never from the previous term's.
- **Device, gate closed:** the IOPS on every input subslot to the PLC goes
  Bad. The PLC sees our data as invalid rather than frozen-good.
- **Device, before the program's first push:** inputs are invalid (IOPS
  Bad) from start-up until the driver has delivered the program's values
  with the gate open. A PLC that connects early sees invalid data, not
  zeros that look good (built, #732).
- A **hung scan** closes the gate within the live-scan lease (about 5 s),
  which is far slower than the PROFINET watchdog (3 × 8 ms). The frame
  keeps flowing on purpose: what protects the process is Stop/Bad, not
  silence. If the process dies, frames stop and the peer's watchdog acts
  within milliseconds.

**Which gate, in this wave.** `naut run` gives a plain gate
(`io.PlainGated`) the live-scan lease when there is no `redundancy:`. It
gives every gated driver leadership when there is. One func cannot carry
both PROFINET behaviours. A standby device must go silent on the network
(§4.5), not merely mark its data invalid while it keeps answering DCP for
the leader's station name. So in this wave:
- both drivers are **plain-gated**: the gate controls the frame contents
  above, and the hung-scan protection works in every project;
- under `redundancy:`, Load marks `profinet-device` a **replica**
  (`WithDeviceReplica`, #790): its gate is leadership, and a standby goes
  silent (§4.5) instead of marking its data invalid. The controller gets
  the same treatment when it lands.

### 4.3 Controller

The sequence per device, in one goroutine, with backoff from 1 s up to
30 s:
1. DCP Identify by station name. Check the vendor and device ID.
2. Optionally DCP Set Name.
3. Connect, carrying:
   - the AR block;
   - the input and output IOCRs, with IODataObjects and IOCS entries at
     computed FrameOffsets;
   - the AlarmCR;
   - the ExpectedSubmodules.
4. Write the parameter records.
5. Send PrmEnd.
6. Answer the device's ApplicationReady.
7. Run cyclic.

What drops the AR:
- the input watchdog (`watchdog-factor` × cycle);
- an RTA abort;
- a Release from the device.

Any of these marks the device Stale and starts the sequence again.
Incoming alarms are acknowledged and counted on the driver row (not
raised as nautilus alarms in this wave).

Cyclic timing uses one sender per interface. It runs on a locked OS
thread and joins the `cpu:` and `priority:` placement the runtime already
has (realtime.md), so real-time work is not duplicated. Send errors back
off: 100 ms doubling to 3.2 s, cycles skipped (the source's be5f926; a dead
NIC otherwise burns two cores).

### 4.4 Device

The port is the source's state machine, plus the rules §1 found missing:
- **Watchdog.** No output frame for max(watchdog, data-hold factor) ×
  cycle, never below 100 ms, ends the AR. The tags go Stale and `__Online`
  goes false. No RTA is sent, because the controller's own watchdog sees
  our silence, and it reconnects on its own. In Advanced Startup the
  first output frame may take up to 10 s after Connect.
- **Run/Stop and IOPS.** DataStatus and IOPS are read on every frame
  (§4.1).
- **ModuleDiff.** The ExpectedSubmodules are compared against the
  manifest. A wrong ident or length is reported in a ModuleDiff block, so
  the engineering tool shows the mismatch instead of the AR running on a
  layout nobody agreed to.
- **Single AR.** A Connect from another controller while an AR runs is
  refused (CMRPC "out of AR resources"). A Connect from the **same**
  controller replaces its AR: it lost the old one (a restart, or its
  watchdog fired before ours).
- **Retransmissions.** DCE/RPC over UDP retransmits a request whose
  response was lost. The device answers a repeat (same activity and
  sequence) from a reply cache and does not execute it twice. A doubled
  Connect would otherwise open a second AR.
- **Fragmentation.** A request split over several RPC fragments is
  ignored with an event; reassembly is not built. Layouts are capped at
  24 subslots so a Connect fits one fragment.

**DCP Set (host network).** DCP is unauthenticated: anything on the
segment can send a Set.
- **Set Name** is accepted in memory, takes effect for Identify, and is
  shown on the driver row with a warning when it differs from the
  manifest. It does not persist: the manifest stays the source of truth
  across restarts.
- **Set IP is never applied to the host.** The tentacle netplan path is
  not ported. A Set IP naming any address the interface holds (a host NIC
  often holds several) is acknowledged, and the device then advertises
  the suite that was set. Any other is refused with a DCP error, and the
  refusal is logged and counted on the driver row with the remedy: give
  the device one of the interface's addresses in the controller's
  project. The device never advertises an address it does not hold.
  Until an IP suite is set it reports none, because a Siemens PLC
  assigns the address itself and will not open an AR to a device that
  reports one on first contact (from the source).
- A DHCP Set is refused: nautilus does not reconfigure the host.
  DCP Control (Signal, which flashes an LED) is acknowledged and logged.

### 4.5 Redundancy

- **Controller.** A device accepts one AR from one controller, so the
  standby holds none. On takeover the new leader runs §4.3, and the
  runtime holds the takeover until the inputs are live (`io.InputsReadier`,
  OptoMMP's passive-standby pattern). The AR setup time is measured on the
  bench and published, never promised.
- **Device.** Two replicas cannot both answer DCP for one station name.
  James chose (2026-11-17), at least to begin with:
  - only the leader runs the device;
  - the standby has no socket open;
  - on takeover, the PLC's AR to the old leader times out;
  - the PLC re-identifies the name, at a new MAC, and reconnects.

  Built in #790. Two things stay open:
  - **The station IP.** The PLC re-assigns the station's IP on takeover,
    and the device accepts that only when the new leader's interface
    already holds the address (§4.4). Two hosts cannot hold one address
    at once, so for now the address has to move with the leader.
  - **Takeover time.** It depends on the PLC and is **measured on the
    bench PLC, never promised**.

  Siemens' own system redundancy (S2/R1) is worth a look once the simple
  case works.

### 4.6 Privileges and ports

Raw Ethernet needs `CAP_NET_RAW`. RPC runs over UDP 34964 on the host IP
stack, so the interface needs an address on the PROFINET subnet.

When either is missing, the driver row says what to do and the driver
retries; a missing capability never crashes `naut run`. The row text
names the remedy:
- `setcap cap_net_raw+ep $(which naut)`;
- or `AmbientCapabilities=CAP_NET_RAW` in the systemd unit.

The guide page documents both.

## 5. Testing

1. **Unit, no socket.** The source's DCP, RPC, PNIO block, GSDML and
   pack/unpack tests come across. Golden-byte tests are added for every
   block both sides build.
2. **Controller against device, in-process.** The transport is an
   interface; `transport.Hub` connects any number of in-memory ports; the
   RPC address is injectable. This is the CI layer, following the
   `modbus/slave` and `eip/logixserver` precedent. It covers:
   - DCP, AR, parameter records, ApplicationReady, and cyclic in both
     directions at real FrameOffsets;
   - watchdog leading to Stale;
   - Stop and bad IOPS leading to Bad;
   - ModuleDiff;
   - gate close and reopen;
   - reconnect with backoff;
   - takeover with `InputsReady`.
3. **Raw sockets on a veth pair.** An unprivileged user and network
   namespace can create the pair without root on a developer machine. On
   GitHub-hosted runners, which restrict unprivileged user namespaces, it
   runs as its own job under sudo, gated on an environment variable like
   the Sparkplug conformance job.
4. **A foreign counterparty.** Steps 1 to 3 are our encoder against our
   decoder (modbus.md §7.5 says why that is not enough). The bench PLC's
   real Connect, PrmEnd, parameter records and cyclic frames are captured
   once, scrubbed of MACs, names and addresses, and checked in as golden
   frames in `testdata/` that the device must answer. PRONETA (free) is a
   second counterparty for DCP and AR on the bench if needed.
5. **Bench.** nautilus as device to the bench PLC. nautilus as controller
   to a PROFINET device. Pull the cable and put the PLC in STOP, then
   record what each side shows and how long it takes.

**Bench checklist: where the port departs from the TIA-proven source.**
Each item is a change from the code that passed a TIA Portal start-up. If
the bench start-up fails, bisect over these first:
- the cycle counter steps by SendClockFactor × ReductionRatio, not 1;
- the I&M0 OrderID and serial are space-padded, not zero-padded;
- each service's own PNIO ErrorCode (0xDB connect, …), not 0xDE for all;
- the LLDP PROFINET port-status TLV is 8 bytes, not 7;
- an Identify answers at once for ResponseDelay ≤ 1, and is delayed
  otherwise;
- write responses are built as IODWriteResHeader, not echoed request
  headers, and multiple write (0xE040) is parsed;
- a ModuleDiffBlock is sent on a mismatch (the source never sent one),
  placed after the AlarmCR response and before ARServerBlockRes;
- ARData alignment counts from the block payload, as the source did;
  check it against the capture;
- inputs start IOPS Bad until the program's first push;
- the 24-subslot cap (to keep a Connect within one RPC fragment) is an
  estimate from block sizes, not a measurement: check the largest
  Connect the PLC sends.

## 6. Wiring

- `internal/project`: two cases in the driver switch, `profinet-controller`
  and `profinet-device`, both taking `manifest:`.
- `drivers.go`: a status row per device or AR.
- The schema enum plus `schema_test`.
- `naut profinet gsdml | tags` (built, #738): both offline, from the
  manifest. A read-only `naut profinet identify` (DCP Identify All on an
  interface, never a Set) comes with the controller.
- `Load` refuses a device manifest that binds data to the PLC under a
  tag the project does not declare as an output (#738): the PLC would
  read its never-written zeros as valid.
- The context map lists `profinet` among the field drivers, and
  `internal/contextmap` holds it to the driver rule.

## 7. Not adopted (this wave)

- IRT and RT_CLASS_3, MRP, and system redundancy (S2/R1). PROFINET's own
  redundancy is not nautilus's (#710).
- PROFIsafe. Safety stays in the hardwired chain (#725).
- PROFINET certification.
- VLAN-tagged frames, and more than one AR per device.
- Raising PROFINET process and diagnosis alarms, and turning incoming ones
  into nautilus alarms.
- Strings and arrays as wire formats.
- Cycle times under 8 ms until measured on a target. The source notes
  that Go GC and kernel jitter trip a 3× watchdog at 1 ms on non-real-time
  Linux. realtime.md's measurements say pinned FIFO can do better, but
  that is a claim to measure, not assume.
- GSDML import (`naut profinet import`). It comes after the bench run, so
  the controller manifest is hand-written first.

## 8. Open questions (for James)

1. ~~**Device under redundancy (§4.5).**~~ Answered 2026-11-17: the
   standby stays silent, at least to begin with (#790).
2. **GSDML identity.** Which VendorID and DeviceID the generated GSDML
   carries.
3. **Bench.** The PLC model and firmware, its network, the host and NIC
   to use, who loads the GSDML and downloads the hardware configuration,
   and which PROFINET device the controller runs against.
