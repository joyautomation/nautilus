# Design: device library

**Context:** Device library (`devprofile`), see the context map. Issue
#717, part of the parity epic #725. Builds on the profile format from
virtual devices (#712, `docs/design/virtual-devices.md` §3). Status:
**in progress**, §8.

## 0. Why

A project maps every device by hand today: a Modbus device map or a
hand-written manifest, a tag per register, an alarm per fault bit, a panel
per piece of equipment, and, since #712, a virtual device beside it. The
same drive in the next project is mapped again, and the four copies of
its register map can disagree.

The library describes a device type once, as a **device profile**, and a
project declares a device by naming one:

```yaml
# nautilus.yaml
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.77.1.101, unit-id: 1 }
  - { name: IOLM, profile: pepperl-fuchs-ice3, endpoint: 10.77.1.103, points: ["Port[12]_*"] }
```

From each row the project gets its driver bindings, its tags (one struct
tag typed by the profile, plus a command tag per writable point), the
profile's default alarms, a faceplate, and a virtual device for tests.

**Done when** (from #717): a project declares a device by profile name and
gets its tags, alarms, faceplate and simulator.

## 1. Decisions

James's, 2026-11-17 (on #717):

1. **Derive at load, from a vendored copy.** `naut devices add` copies a
   profile, with its simulator, into the project's `profiles/` folder.
   nautilus derives bindings, tags and alarms from that copy when it loads
   the project. Nothing is generated into other files, so a device is one
   row to edit (which suits #706 and #711, configuration as data). A naut
   upgrade never changes a deployed controller's register map: the copy
   is the pin. `naut devices update` replaces a copy explicitly, showing
   the diff first.
2. **One struct tag per device, plus command tags.** `CONV` is a tag of a
   type generated from the profile. Its members read back what the device
   holds. Each writable point also gets an output tag, `CONV_ControlWord`,
   which the program writes. This is the rule the SNMP, Redfish and
   Sparkplug host drivers already follow for writable members
   (`hw.WriteDecl`): the struct tag stays an input, and a member shows the
   device's value, not the program's intent.

Following from them:

- **Sharing is a folder under git.** There is no registry (#193 was
  declined: reuse across projects is a folder under version control).
  Built-in profiles ship in naut and are the usual source for `add`; a
  team's own profiles are a directory `add` can copy from just as well.
- **The profile's `version:`** is the copy's own revision. It is not
  used at load; `update` reports it.

**What `naut devices update` compares.** It compares what a profile
*produces*, never YAML text: the struct type (members, their types and
order), the tags (struct and command tags), the driver bindings
(table, address, format, scale, offset, word and byte order, unit id;
for OPC UA the node id and namespace), the derived points and the
alarms. For each vendored profile it reports:

- **the copy against the source:** what would change in the project if the
  source replaced the copy;
- **a hand-edited copy:** the copy produces something no released version
  of the source produces. `update` names those rows and does not
  overwrite them without `--yes`;
- **a changed profile with no new `version:`:** the source's output
  differs from the copy's at the same version number. That is reported
  as a fault of the source, so a team bumps the version;
- **a shared simulator file** (`abb-drives.st` serves three ABB profiles):
  it is replaced only when every profile vendored with it moves to the
  same source, else `update` refuses and names the profiles;
- **a removed or retyped member still in use:** `update` runs the
  project's check with the new copy and refuses while a program reads a
  member the new profile no longer has (or has with another type),
  naming the program and line.

## 2. A project's devices

A `devices:` row has:

| Key | Meaning |
|---|---|
| `name` | the device's tag name (an IEC identifier) |
| `profile` | the profile id; resolved in the project's `profiles/` folder only |
| `endpoint` | Modbus `host[:port]`, or OPC UA `opc.tcp://host[:port]` or a bare host |
| `unit-id` | Modbus unit id; defaults from the profile; an OPC UA device has none |
| `points` | globs over point names: poll and expose only these (an 8-port master need not poll 8 ports); default all |
| `params` | values for the profile's `params:` (an ifm master's location) |
| `desc` | one line for the tag and the faceplate |
| `driver` | an OPC UA device's connection: an opcua-client template section of `drivers:` (below) |
| `validate` | `live` or `static`: how a live I/O configuration change checks the device (#706). Overrides the driver's. Default: `static` when the profile documents `modbus.max-connections` of 2 or fewer, else `live`. `live` on a profile documenting one connection is an error, since the check's connection would be the controller's. devbind gives #706 the row's setting, the default and why (`Set.Validate`); #706 applies row, then driver, then default |
| `ports` | an IO-Link master's ports by number, `{ 2: { name: LEVEL1, iodd: sensor.xml } }` (#705): each port's name shares the devices' one namespace, and its IODD is a file in the project's `iodd/` folder. Only a profile that declares IO-Link ports takes them, and none does until #705 adds the profile's `iolink:` block, so until then a row's `ports:` is refused rather than ignored |

A row has no driver keys of its own. A Modbus device polls at the scan
rate and scan classes of the project's Modbus driver (`drivers:`, one is
added if there is none), which is where those keys are written.

An OPC UA device is a driver of its own (§3.1). A row with no `driver:`
connects with security None and anonymous access, which is what the
profiles document their servers offering, and `naut check` warns per row
("device X connects with security None, anonymous") so that a device
offering more is not downgraded without a word. A row that names a
`driver:` section (#975) connects as that section says:

```yaml
drivers:
  - { name: iolm-secure, type: opcua-client, security: Basic256Sha256,
      security-mode: SignAndEncrypt, pki-dir: pki/opcua-client, username: plc }
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: 10.0.0.21, driver: iolm-secure }
```

- The section is a **template**: an opcua-client section with no
  `endpoint` (the row's) and no `manifest` (derived). It is never built or
  started; each device that names it gets its own driver, named after the
  device, with the section's `security`, `security-mode`, `pki-dir`,
  `username`, `read-mode` and `scan-rate` (no other key: `scan-classes`
  and `tag-classes` would match derived binding names, and `points:`
  selects what is polled). The password is the opcua-client driver's,
  `NAUTILUS_OPCUA_PASSWORD`, never in a file.
- Any number of rows may share a section; each is its own session, and
  they share the client certificate in its `pki-dir` (generated once, on
  first use, under a lock, so drivers starting together never write a key
  and a certificate that do not match).
- **Trust runs both ways.** The device must trust the project's client
  certificate (`<pki-dir>/cert.pem`): until an operator approves it on the
  device, a secured session is refused, and the driver's error says which
  file to trust. The client trusts a device's server certificate **on first
  use** and pins it (`<pki-dir>/trusted-servers/<host>_<port>.sha256`,
  `opcua/trust.go`): a later certificate that differs is refused ("server
  certificate changed") until that file is deleted, and a server once
  pinned on a secured endpoint never gets an unsecured session.
- No None warning for such a row: the security is the author's. A section
  that sets None (as `security:` or `security-mode:`) is still said once
  ("device X connects with security None, as its driver: section S sets"), and the driver's own findings
  (no `pki-dir` for the default strongest endpoint, a username without
  Sign) appear under the device's name.
- `driver:` on a Modbus row is an error for now; it is kept for naming
  which Modbus driver a device joins.

A project names a point its own way with the existing `alias:` (`BinHigh`
aliases `IOLM.Port1_SioInput`), not a new rename key.

A row whose profile is not in `profiles/` is an error that says to run
`naut devices add`. A derived tag or source that collides with a
hand-declared one is an error naming both.

## 3. What a row derives

### 3.1 Driver bindings

- **Modbus:** one source per device (`endpoint`, `unit-id`, the profile's
  word and byte order) and one binding per selected point, merged into the
  project's Modbus driver. If the project declares none, one is added.
- **OPC UA:** one opcua-client driver per OPC UA device, since an OPC UA
  client talks to one server. The driver is named after the device, so its
  companion is `<Device>__Online`, as a Modbus device's source is. A
  device whose name is already a driver's is an error. The driver has a
  binding per selected point: the node id with the row's `params:`
  substituted, and the OPC UA type from the point's IEC type (BOOL is
  Boolean, WORD is UInt16, DWORD is UInt32, REAL is Float, and so on). It
  also has a binding per writable point under its command tag's name,
  written and never polled (scan class `none`), the OPC UA spelling of a
  Modbus command's write-only binding.
- **Namespaces:** a node's namespace URI comes from the profile's
  namespace array when the profile's URI is verified. The driver then
  resolves the index against the server's live table at connect, so a
  server that renumbers its namespaces is still read correctly. A URI
  starting `urn:virtual:` is a stand-in the profile made up because the
  maker does not publish the real one, so the binding trusts the index the
  profile documents instead. That is a known risk, not a silent one:
  `naut check` warns "device X binds by namespace index N". Every shipped
  OPC UA profile's URIs are stand-ins today. A node id in a namespace
  past the profile's array is refused when the profile is read.

The drivers are only ever handed manifests, the same structures their own
files decode to. No change to `modbus/` or `opcua/`.

### 3.2 Tags and the struct type

The device's type is a `STRUCT` with one member per **selected** point
(all points without `points:`), named as the point, of the point's IEC
type, in profile order. A point the row does not select is not a member,
so a program cannot read a value that is never polled. Its name is the
profile's `udt:` key, or the id upper-cased with `-` as `_`
(`ABB_ACS355_FMBT21`). Devices of one type must select the same points;
two profiles whose selected points are the same (the ACS550 and ACS355
Classic layouts) may declare one `udt:`, and one name over different
members is an error. A `udt:` may not be a standard type or block name
(`TON`, `WORD`) or a device's name.

- `CONV : ABB_ACS355_FMBT21`, role input. Every selected point is a member
  and reads back the device.
- `CONV_ControlWord`, role output, of the point's type, for every writable
  point.
- `CONV__Online` (BOOL), and for a Modbus device `CONV__Outages` (DINT),
  role input: the driver's companions for the device, declared by the row
  so a program can gate on them. Being declared tags, they are also in the
  Sparkplug DBIRTH with the device's other tags. A hand-declared tag of the
  same name is a load error naming both.

The types are composed by `internal/stproject` beside the dialect's
block library (`nautilus.yaml#devices`), so every tool that compiles the
project knows them. An **adapter** between the drivers and the runtime
makes the tags work without driver changes:

- **Inputs:** the drivers bind each point under an internal flat name.
  The adapter assembles the struct value from those names and delivers it
  under the device's name.
- **Outputs:** the runtime pushes the command tags, and the adapter
  passes each one to its point's binding.

The adapter is the device library's, composed by `internal/project` the
way other drivers are.

**Quality and write gating.** Quality is reported per device, not per
member: the struct tag is NotConnected until a polled member arrives, Bad
if any polled member is Bad or has never arrived while others have, else
Stale if any is, else Good. One bad register therefore greys the whole
device, which is what an operator should see: part of the device's data is
not to be trusted. Writes are gated per driver, as today: the Modbus
driver's write gate (redundancy, a faulted scan) holds every command tag
of every device on it. A command tag no program has written is never
pushed, so a device's unused commands never reach it, not even on a
standby's takeover.

### 3.3 Derived points

Alarms are BOOL-only, and many faults are bits of a word or a code. A
profile may declare **derived points**, a `derived:` list beside `points:`:

```yaml
derived:
  - { name: Tripped, from: StatusWord, bit: 3, doc: "fena pp. 93-94" }
  - { name: SensorBreak, from: Loop1Error, eq: 39, doc: "mct4 p. 172 register 39, *B11 p. 176" }
```

`from` names a polled integer point (INT UINT WORD DINT UDINT DWORD, not
another derived point); exactly one of `bit` (inside the source's width) and
`eq` is given; each cites a document or says `unverified`, as a point does.
(It is `derived:` and not a `from:` on a point because a point's `from` is
the repeat start.)

- They are BOOL members of the struct, after the polled ones, computed by
  the adapter when the source arrives; they are never polled, written or in a
  manifest. A virtual device's simulator keeps only the source point, so the
  controller computes them from the register the way it does from a real
  device. A derived point is false until its source has been delivered, so
  `eq: 0` is not true for a register the device never answered.
- **On comms loss** a derived point holds its last value, exactly as its
  source member does (a delivery without the source recomputes from the held
  value); the device's quality and `<Device>__Online` say the data is old.
  `eq` must be a value the source's type can hold, or the profile is refused
  (it could never be true).
- A row's `points:` globs match derived names too. **Selecting a derived
  point selects its source** (the struct has no member the adapter could not
  compute), so `points: [Tripped]` gives `StatusWord` and `Tripped`.
- Devices of one type must have the same members, derived included.
- **Deploy targets.** The adapter is the nautilus runtime's. Neither the
  Logix nor the Siemens writer carries `devices:` rows: the device type would
  be written and nothing on the controller would fill it, so no member (a
  derived one included) could be trusted. `naut check` against either target
  therefore refuses a project with `devices:` rows, naming the gap.
- `naut devices render` lists them under `# derived`.

### 3.4 Alarms

A profile's `alarms:` names BOOL points (often derived), with the alarm
fields (`name`, `priority`, `on-delay`, `class`, `notes`):

```yaml
alarms:
  - { point: Tripped, name: "{desc} tripped", priority: high, class: drive }
```

Each renders as an alarm rule over the profile's type (`match: {type,
member}`), so one rule covers every device of that type that selects the
point (a row whose `points:` leaves it out has no such alarm). Alarms still
have to be on: the project needs an `alarms:` section.

- **A priority left out** is what any alarm with none gets: the project's
  `alarms.defaults.priority`, else medium.
- **A project's own `alarms:` win.** The device rules are matched after the
  manifest's, so a project rule on the same member (first match wins) makes
  the alarm instead; a hand-written alarm with the id a device rule generates
  is the ordinary duplicate-id error, naming both sources (`devices:` and
  the project's alarm).
- **While the device does not answer**, its alarms are suppressed, not
  frozen on: each rule carries `enable: "{tag}__Online"`, #715's designed
  suppression (the same as an IO-Link port's identity fault with its
  master). An alarm that cannot be evaluated is not an alarm of nothing, so
  every `devices:` row also raises a **comms-loss alarm**, `<Device>__Offline`:
  high priority, class `comms`, 5 s on-delay, raised while `<Device>__Online`
  is false and never suppressed by the device itself. (It is a system alarm
  because an alarm's condition is true-is-alarm and the driver's companion is
  true-is-healthy.) Equipment out of service (#715) covers it with the
  device's other alarms; the equipment is the device's name.
- **Tuning it:** a row's `offline-alarm:` takes `{priority, on-delay}`, or
  `false` to turn it off on purpose (a typo or a bad value is a load error,
  and `naut check` warns on every run that a device has none).

Natural alarms for the first devices:

| Device | Alarm | Point |
|---|---|---|
| ABB drives | tripped | status word bit 3 (`Tripped`) |
| FDC MCT4 | sensor break | loop 1 error code 39 (`SensorBreak`) |
| IO-Link masters | port fault, where the master exposes one | per-port status |
| relay boards | none of their own | the comms-loss alarm |

### 3.5 Faceplate

A device's faceplate is a generated `MimicPanel` (the data-driven panel
`hmi/src/lib/mimic.ts` already renders):

- **readings** are read-only numeric members, with unit and range;
- **setpoints** are numeric command tags;
- **commands** and **toggles** are BOOL command tags.

A profile's optional `faceplate:` key orders and groups points; without
it, the points' roles decide. `naut hmi build` adds a page with every
device's faceplate. A mimic's equipment can say `device: CONV` to take
that panel instead of writing one. The mimic's `bind` does not resolve
struct members yet (`docs/design/examples-dogfood.md`); this work closes
that gap. #714's asset pages can take the same data.

### 3.6 Virtual devices

A project with `devices:` gets virtual devices from the same rows, with or
without `virtual-devices.yaml`. Each simulates the vendored copy (the one
the controller derives from) and the whole device, whatever `points:`
selects. `virtual-devices.yaml` keeps the process model, devices the
manifest does not declare, and the simulation-only keys (`listen`, `init`,
`opcua`) of a declared device by name; repeating a row's `profile`, `endpoint`,
`unit-id` or `params` there is an error. `naut test`'s in-process face
delivers a device, Modbus or OPC UA, through a fresh copy of the adapter's
own input side, so a sequence test proves the adapter that ships. `naut build` ships the
vendored profiles and leaves the simulators and the model behind. A
project without `devices:` keeps working as #712 left it.

## 4. Not a tautology

When the controller's bindings and the simulator come from one profile, a
sequence test cannot catch a wrong register **in the profile**: both sides
agree on it. A profile's correctness therefore rests on:

1. **Citations:** every point cites a document and page, or says
   `unverified: true`.
2. **An independent check:** someone (or a second agent) who did not write
   the profile verifies each cited row against the document.
3. **Hand-mapped fixtures:** `examples/bin-fill` keeps its hand-written
   manifests, so its tests still catch a mapping mistake. The library gets
   its own example.
4. **Real devices**, when there are any, through the wire face's
   agreement test.

The sequence test still proves the logic, the scaling direction and the
start-up sequence against a device that behaves.

## 5. Writing a profile with AI

naut does not call a model. Instead:

- `naut devices lint <profile>` checks:
  - every row cites a `docs:` entry or says `unverified`;
  - no two points overlap an address;
  - `type` and `format` agree;
  - derived points name a point that exists;
  - the simulator compiles;
  - the alarms name BOOL points.
- The built-in AGENTS.md gets a section on writing a profile from a
  manual: cite every row, mark guesses `unverified`, write the simulator
  from the manual's state machine, run lint.
- The independent check (§4.2) is a documented step.

## 6. Commands

| Command | Does |
|---|---|
| `naut devices add <profile> [--from dir]` | copy a profile (and its simulator) into `profiles/` |
| `naut devices update [<profile>] [--from dir]` | compare what the copy and the source produce (§1); replace with `--yes` |
| `naut devices render [dir]` | print what each row derives: bindings, the type, tags, alarms |
| `naut devices lint <profile>` | §5 |
| `naut devices profiles`, `show`, `up`, `check` | as in #712 |

`naut build` ships `profiles/*.yaml` (load needs them) but not simulators
or the process model.

## 7. Not here

- IODD import and IO-Link port identity: #705.
- The asset page: #714 (it consumes §3.5's data).
- GSDML: #723. CIP assemblies: later.
- A package registry: declined on #193.

## 8. Plan

1. This brief. The profile format and built-in library move from `virtdev`
   to `devprofile` (a leaf package, context map row and rule). No
   behaviour change. (#805)
2. `devices:` for Modbus devices:
   - `devbind`: the struct type, the tags, the driver manifest, and the
     adapter that delivers a device as one struct;
   - `internal/stproject` composes the device types, so `naut check`,
     `run`, `build`, `test` and the LSP all know them;
   - `naut devices add` and `render`;
   - the manifest JSON schema;
   - `naut build` ships `profiles/*.yaml` but not the simulators.
3. Virtual devices from the `devices:` rows: the plant, the wire face and
   the in-process face, with the struct adapter in `naut test`.
4. OPC UA devices.
5. Derived points, alarms, faceplates (and struct members in mimic
   `bind`).
6. `update` and `lint`, the AGENTS.md section, and an example.
