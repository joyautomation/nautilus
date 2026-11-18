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
- **The profile's `version:`** is the copy's own revision. `update`
  compares it with the source's and shows both.

## 2. A project's devices

A `devices:` row has:

| Key | Meaning |
|---|---|
| `name` | the device's tag name (an IEC identifier) |
| `profile` | the profile id; resolved in the project's `profiles/` folder only |
| `endpoint` | Modbus `host[:port]`, or OPC UA `opc.tcp://host[:port]` or a bare host |
| `unit-id` | Modbus unit id; defaults from the profile |
| `points` | globs over point names: poll and expose only these (an 8-port master need not poll 8 ports); default all |
| `params` | values for the profile's `params:` (an ifm master's location) |
| `desc` | one line for the tag and the faceplate |
| driver keys | `scan-rate`, `timeout`, `security`… passed to the driver the device lands on |

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
- **OPC UA:** one opcua-client driver per OPC UA device, with a binding per
  selected point (node id, namespace URI from the profile's namespace
  array, type from the point's IEC type).

The drivers are only ever handed manifests, the same structures their own
files decode to. No change to `modbus/` or `opcua/`.

### 3.2 Tags and the struct type

The profile's type is a `STRUCT` with one member per point, named as the
point, of the point's IEC type. Its name is the profile's `udt:` key, or
the id upper-cased with `-` as `_` (`ABB_ACS355_FMBT21`). Two profiles
whose points are the same (the ACS550 and ACS355 Classic layouts) may
declare one `udt:`. Two vendored profiles declaring one name with
different members is an error.

- `CONV : ABB_ACS355_FMBT21`, role input. Every selected point is a member
  and reads back the device.
- `CONV_ControlWord`, role output, of the point's type, for every writable
  point.
- The drivers' `__Online` and `__Outages` companions stay as they are.

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

### 3.3 Derived points

Alarms are BOOL-only, and many faults are bits of a word or a code. A
profile may declare **derived points**:

```yaml
- { name: Tripped, type: BOOL, from: StatusWord, bit: 3, doc: "fena p. 95" }
- { name: SensorBreak, type: BOOL, from: Loop1Error, eq: 39, doc: "mct4 p. 176 *B11" }
```

They are members of the struct, computed by the adapter from the point
they name; they are never polled, and a virtual device computes them
the same way.

### 3.4 Alarms

A profile's `alarms:` names BOOL points (often derived), with the alarm
fields (`priority`, `name`, `on-delay`, `class`):

```yaml
alarms:
  - { point: Tripped, name: "{desc} tripped", priority: high }
```

Each renders as an alarm rule over the profile's type (`match: {type,
member}`), so one rule covers every device of that type. A project's own
`alarms:` can override a rule by id or turn it off with `enable`.
`{tag}__Online` comms-loss alarms come from the existing companions.

Natural alarms for the first devices:

| Device | Alarm | Point |
|---|---|---|
| ABB drives | tripped | status word bit 3 |
| FDC MCT4 | sensor break | loop 1 error code 39 |
| IO-Link masters | port fault, where the master exposes one | per-port status |
| relay boards | none of their own | comms loss through `__Online` |

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

A project with `devices:` gets virtual devices from the same rows.
`virtual-devices.yaml` keeps only the process model and simulation-only
keys (`listen`, `init`) by device name. A project without `devices:` keeps
working as #712 left it.

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
| `naut devices update [<profile>] [--from dir]` | show the diff against the source; replace with `--yes` |
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
