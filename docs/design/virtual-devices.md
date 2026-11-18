# Design: virtual devices

**Context:** Virtual devices (`virtdev`), see the context map. Issue #712,
part of the parity epic #725. Status: **in progress.** The format, the plant,
the Modbus wire face and `naut devices` (PR 1); the OPC UA wire face and
three makers' IO-Link masters (PR 2); ABB drives, the in-process face in
`naut test` and a sequence test (PR 3); the burner's temperature
controller (PR 4) are built. The OPC 30120 base profile and `naut run
--devices` follow (§7).

## 0. Why

A project's field devices are what its tests cannot reach. `naut test`
proves the logic against a memory driver: it never sees the register map,
the scaling, the word order or the node ids the project's drivers use, and
it cannot show that a drive comes up when the control word sequence is
right. Commissioning practice needs the devices themselves.

A virtual device stands in for one field device and speaks its protocol,
register map or address space: an IO-Link master's OPC UA server, a Modbus
relay board, a drive's register layout, a burner controller. It is built
from the device's public documentation, and every row says which document
and page it came from. A project lists its devices; one command brings
them up; a sequence test passes against them.

## 1. One device, two faces

A virtual device is a **store** (a Modbus unit's register and coil tables;
later, an OPC UA server's node values) plus, optionally, a **simulator** that
gives it behaviour. The store has two faces:

1. **Wire face**, wall clock, real sockets. The store is served by
   `modbus/slave` (or the OPC UA server, PR 2) on a listen address. This is
   for commissioning practice, for black-box tests of any controller (a
   CODESYS program today, its nautilus replacement later) and for the OPC UA
   client's test matrix (#704). `naut devices up`, `naut run --devices`.
2. **In-process face**, virtual time, no sockets (PR 3). `naut test` keeps
   its rule that it never opens a socket (testing.md §4.3). A suite that
   opts in (`devices: true`) gets, instead of the bare memory driver, one
   that resolves the project's *own* driver bindings against the device
   stores through the same codec, while the acceptance scheduler steps the
   devices and the process model on the same virtual clock. A sequence test
   is then deterministic and still proves the register map, scaling and node
   ids the project uses.

Why not sockets inside `naut test`: the Modbus and OPC UA drivers poll on
their own wall-clock goroutines, so a test through them cannot be
deterministic, and it would break a promise `naut test` makes to every
project today. The wire face gets its own end-to-end test instead.

## 2. The plant

All of a project's devices run in one **plant**: an ordinary nautilus
runtime (`virtdev.Plant`), so its clock is injectable and its simulators are
ST compiled by the same compiler.

- Every point of every device is a plant tag `<Device>_<Point>`. A point the
  controller writes (a command, a reference, a relay) is a plant **input**,
  delivered from the device's store before each main scan. A point the
  device shows is a plant **output**, written into the store after it.
- The main task (`devices`) is a generated program: one simulator instance
  per device that has one, called with its inputs, its outputs copied back.
- A simulator's VAR_INPUTs and VAR_OUTPUTs that are not points are
  **hooks**: plant tags `<Device>_<Var>` the process model writes (a drive's
  load) or reads (a drive's running state).
- The **process model** is the project's own ST PROGRAM, run as a second
  task (`model`). It reads what the devices command and writes what they
  sense: a relay drives a gate, a gate fills a bin, a level reaches a plug
  switch wired to a relay board input. It is only as rich as a project's
  tests need.

The plant is never part of a controller. `virtdev` is imported only by the
CLI and by Verification (context map §2.1).

## 3. Device profiles: the format

One YAML file per device type. This is the format proposed to the
device-profile library (#717), which will add the parts a running project
needs (UDT, alarms, faceplate) around the same points.

```yaml
profile: abb-drive-fmbt21          # id: what virtual-devices.yaml refers to
title: ABB drive, Modbus/TCP via FMBT-21, ABB Drives profile
maker: ABB
model: ACS380 / ACS580 with FMBT-21
version: 1                          # the profile's own revision
protocol: modbus                    # modbus | opcua
docs:                               # provenance: every row cites one
  - { id: fmbt21, title: "FMBT-21 Modbus/TCP adapter module user's manual", code: "…", url: "…" }
modbus: { unit-id: 1, word-order: big }   # port defaults to 502
points:
  - { name: ControlWord, type: WORD, table: holding, address: 0, writable: true,
      desc: "ABB Drives profile control word", doc: "fmbt21 §… p. …" }
  - { name: "Relay{n}", type: BOOL, repeat: 16, table: coil, address: 0, writable: true,
      desc: "Relay output {n}", unverified: true }
simulator: { block: ABB_DRIVE_SIM, source: abb-drive-fmbt21.st }
notes: what is simplified and what is not modelled
```

- **Point columns reuse the driver manifests' names**: `table`, `address`,
  `format`, `scale`, `offset`, `writable` as in `modbus.TagBinding`; the OPC
  UA rows (PR 2) take `node-id`, `namespace` and `type` as in
  `opcua.TagBinding`, plus a browse `path` the server builds its tree from.
  A profile can therefore be rendered into the manifest a project's driver
  reads, and into the UDT a project's logic uses, from one file.
- **OPC UA rows** carry `node-id` (index into the profile's
  `opcua.namespaces`, so the ids land at the indices the device uses: a
  client that addresses nodes by index, as CODESYS does, finds them) and
  `path` (the browse path under Objects; its last element is the
  BrowseName). The Objects on a path get ids from `opcua.objects`, or
  `ns=<object-namespace>;s=<path>`. A repeated numeric id advances by
  `stride`. `params:` declares per-instance values a node id refers to as
  `{name}` (an ifm master's location prefix).
- `type` is the IEC type; `format` defaults from it (`WORD` → `uint16`,
  `REAL` → `float32`); a scaled point is `REAL` or `LREAL`.
- `repeat: N` expands a row N times, `{n}` substituted and the address
  advanced by `stride` (default: one value's width).
- **Provenance is mandatory**: every row cites a `docs:` entry with a
  section or page, or says `unverified: true`. Unverified rows keep a device
  working where the public documents are silent, and `naut devices show`
  prints them as UNVERIFIED so nobody takes them for fact.
- A Modbus device answers like the hardware: an address no point covers,
  or a write to a read-only one, gets exception 02 (`modbus.open: true`
  turns that off for a device whose documents do not say).
- A point **both sides write** (a loop's %Out: the controller's in manual,
  the device's in auto) is a writable point the simulator declares
  `VAR_IN_OUT`: each scan it gets what the register holds, and the register
  gets what it leaves, so a controller's write lands and the device's own
  value replaces it unless the simulator keeps it. Modbus devices only.
- A simulator's `VAR_INPUT Dt : REAL` gets the plant's scan seconds; the
  process model task has `Model_Dt`.
- The **simulator** is an ST FUNCTION_BLOCK beside the profile. ST because
  it reuses the compiler, `naut check` and virtual time, and because it is
  what an agent will write when profiles are generated from a manual.
- Built-in profiles ship in naut (`virtdev/profiles/`); a project's own
  profile is a path ending in `.yaml`.

## 4. A project's devices: `virtual-devices.yaml`

```yaml
devices:
  - name: RB1                    # IEC identifier; its points are RB1_<Point>
    profile: asi-275m
    endpoint: 192.168.10.21      # the address the project's driver dials
    listen: 127.0.0.1:15021      # wire face; default an ephemeral loopback port
    init: { Input3: true }       # power-up values, simulator hooks
scan: 50ms                       # the simulators' task
model:
  program: plant.st              # the process model
  scan: 100ms
```

`virtual-devices.yaml` is dev-time, like `*_test.yaml`, and stays out of
`nautilus.yaml` (the deployment artifact). An entry's first three keys are
the shape a project will declare its *real* devices with once profiles live
in `nautilus.yaml` (#717): name, profile, endpoint. At that point the
virtual devices derive from the manifest, and `virtual-devices.yaml` keeps only what
a simulation adds (listen, init, the model).

`endpoint` is matched against the project's own driver configuration, so
`nautilus.yaml` is never edited to test: the in-process face answers there,
and `naut run --devices` redirects that address to the wire face in memory.
Modbus devices that share an endpoint (unit ids behind one gateway) share
one listener, as in the field.

## 4a. The in-process face, as built

`virtdev.InProcess` is the driver a `devices: true` suite runs on, built
fresh per test (`acceptance.WithDevices`) over a fresh plant on the test's
virtual clock. It reads through the project's drivers as configured
(`Project.Drivers()`): a Modbus driver's planned block reads go to the
device's unit as requests (`slave.Unit.Handle`, the code the wire face
answers with), each binding decoded with its source's word and byte order
and scale; writes go as FC 05 / FC 10 requests; an OPC UA binding resolves
its node id (by namespace URI when it names one) against the device's
mapped address space. A source or endpoint no device answers is refused
before the first scan; a refused request is a fault that fails the test.
The real drivers' companions (`<source>__Online`, `__Outages`, an OPC UA
driver's `__Online`) are delivered. Before every read the plant is stepped
to the controller's virtual time, so two runs are identical.

It also keeps the Modbus driver's start-up baseline: the first output push
is recorded and never sent, and after it only changes go out. That is the
trap a start sequence falls into when it relies on its first-scan control
word reaching a drive (the bin-fill tests keep a case of it).

**What it does not model, by design:** poll timing. Every binding is read on
every controller scan, where the real driver polls each scan class at its
own rate over a network with latency; a test that depends on a value
arriving late, or on two classes arriving in a given order, belongs on the
wire face (`naut devices up`, wall clock). The agreement test
(`virtdev/agree_test.go`) runs the project's real drivers against the wire
face and checks the two faces deliver the same values for the same writes.

A step's `plant:` writes a plant tag: a device point, a simulator hook
(`CONV_Trip`) or the model's own. That is how a sequence test plugs a
conveyor or trips a drive.

`naut check` knows the file too: the process model and project-local
simulators are compiled as the plant, not as controller programs.

The worked example is `examples/bin-fill`: an ACS355 conveyor
drive, an ASI-275M gate, a P+F master with the high-level and plug
switches, a process model in `devices/plant.st`, and `fill_test.yaml`. The
Go tests in `cmd/naut/devices_test.go` run it, and run copies with a wrong
register, a mistyped node id, a wrong scale and a wrong unit id, each of
which fails.

## 5. Commands

| Command | Does |
|---|---|
| `naut devices up [dir]` | every device on its wire face, plant in wall time, until ctrl-c; prints the listen address of each |
| `naut devices check [dir]` | load and compile the plant; no sockets |
| `naut devices profiles` | the built-in profiles |
| `naut devices show <profile>` | a profile's points, with the document each row came from |
| `naut run --devices` (PR 3) | the devices, plus the project with its drivers redirected to them |
| `naut test` | suites with `devices: true` run against the in-process face |

## 5a. OPC UA wire face

An OPC UA device is served by `opcua/server` over the plant's tag store,
with `Options.Mapped` laying the device's address space over it
(`opcua/server/mapped.go`): the device's namespaces first, at its indices;
its tree under Objects instead of the project folder; each Variable a plant
tag, so Read, Write and its checks, AccessLevel, subscriptions and the
change sweep are the server's own, unchanged. BuildInfo names the device's
maker and model. Security None, anonymous; a writable point is open to
anonymous writes, as the masters are in their default setup. Sweep 20 ms.

### IO-Link masters (for the OPC UA client's test matrix, #704)

The masters the reference plant reads, at the node ids its controller uses.
`naut devices up` prints each one's listen URL; `naut devices show <profile>`
lists every node.

| Profile | Port | Node ids | Types | Provenance |
|---|---|---|---|---|
| `pepperl-fuchs-ice3` | 4840 | `ns=1;s=IOLM/Port {n}/SIO Input`, `…/Status`, `…/Mode`, `…/Attached Device/PDI Data Unsigned32`, `…/PDO Data Unsigned32` (writable), `…/PDO Valid` (writable), `ns=1;s=IOLM/Model` …; n = 1–8 | Boolean, String, UInt32 | P+F TDOCT-6418D §12.2, all rows; Objects on the path follow the table's scheme |
| `sick-sig300` | 4840 | `ns=3;i=8008+4n` port n pin 4, `ns=3;i=8007+4n` port n pin 2; n = 1–8 | Boolean | ids a deployed controller's program reads; browse names, paths and namespace URIs **unverified** |
| `ifm-al1590` | 4843 | `ns=5;s={location}/iolinkmaster/port[{n}]/iolinkdevice/pdin`; n = 1–8 | String (hex text, `'0286'`) | ids a deployed controller's program reads; tree per ifm AL1427 manual §10.4.3 |

Namespace URIs are not published by any of the three makers and are
stand-ins (`urn:virtual:…`); a client should address these nodes by index
or by the ids above, as the real ones are addressed. The OPC 30120 base
profile (IOLinkMasterType and friends, from the companion NodeSet) is the
next step: gopcua's NodeSet import does not remap namespace indices, so it
needs an importer that does.

## 5b. The burner controller

The reference plant's burner is fired by a temperature controller on
Modbus, a Future Design Controls MCT4; flame supervision (prepurge,
ignition, flame proving, lockout) is a separate burner manager that is not
on the controller's network. So the virtual "burner controller" is
`fdc-mct4`: the MCT4's Modbus TCP map from its public manual (MCT4 4.3 User
Manual Rev. G, App. A.1, pp. 164-177), loop 1 as a PI loop over a
first-order thermal model that heats only while the burner manager reports
a flame (hook `FlameOn`). %Out is a shared point (VAR_IN_OUT): written by
the controller in manual, by the loop in auto. A sensor break reads error
code 39 and drops the output. Loops 2-3, the limit, alarms, events,
autotune and automatic programs are not modelled (profile notes).

## 6. Not built here

- **AI generation** of a profile and its simulator from a manual, register
  map, IODD, GSDML or EDS, with an independent check against the document.
  The format is shaped for it (ST simulator, per-row provenance).
- **EtherNet/IP** (the Logix emulator, a CIP adapter) and **PROFINET**
  virtual devices.
- **A process-model library** beyond what the first sequence test needs.
- **The device-profile library** itself (#717): versioning, sharing, UDT,
  alarms, faceplate. This brief proposes the format only.

## 7. Plan

1. This brief, the format, the plant, the Modbus wire face, `naut devices`,
   a 16-in/16-out relay board.
2. IO-Link masters over OPC UA: the mapped address space in
   `opcua/server`, P+F, SICK and ifm variants; endpoints and node ids for
   #704 (§5a). The OPC 30120 base profile with the companion NodeSet's types
   follows in its own PR.
3. ABB drives (ACS550 + RETA-01 and ACS355 + FMBT-21 Classic, ACS580 +
   FMBT-21 Enhanced), the in-process face in `naut test`, `plant:`, the
   bin-fill sequence test.
4. The burner's temperature controller (`fdc-mct4`), with shared points.
5. The OPC 30120 base profile; `naut run --devices` (needs a
   driver-endpoint override at project load); the language server knowing
   the plant's tags.
