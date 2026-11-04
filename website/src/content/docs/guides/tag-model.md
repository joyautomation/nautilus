---
title: The tag model
description: The tag store is the seam everything meets at — how tags come to exist, which role fits which job, and the one rule that bites.
---

The tag store is the seam everything meets at: the IEC program, the field
driver, the HMI/API, and the editor's live values all read and write the
same named values. One scan looks like this:

```
driver.ReadInputs() ──(Input tags)──▶ ┌───────────┐ ──(Output tags)──▶ driver.WriteOutputs()
                                      │ tag store │
HMI / POST /api/tags ──(writes)─────▶ │           │ ──(reads)────────▶ HMI / editor pills
                                      └───────────┘
                                        ▲       │
                                 program writes │ program reads
                                     (coils)    ▼
```

Every tag in `nautilus.yaml` has a `role:`, and choosing it is the first
decision about a tag: the role says who owns the value. The next section
answers that in manifest terms. The rest of the page explains the rules
underneath, and [the Go form](#declaring-tags-one-entry-per-tag) shows the
same roles in an SDK project.

## Which role fits which job

A tag's `role:` is one of `input`, `output`, `setpoint` or `state`. Any
other value is a load error (`tag Pump: role must be input, output,
setpoint, or state (got "command")`). Pick the role by who owns the value:

| You want a tag that… | `role:` | It also needs |
| --- | --- | --- |
| comes from a sensor, through a driver | `input` | a driver binding that delivers it |
| goes to an actuator, through a driver | `output` | a program that writes it |
| goes to an actuator, and the logic also reads it (a seal-in) | `output` | an `init:`, so it exists on scan one |
| an operator adjusts: a setpoint, a gain, a limit | `setpoint` | an `init:` or a `type:` |
| the logic keeps across scans or computes for the HMI: a mode word, an integrator, an alarm | `state` | an `init:` or a `type:` |
| the logic keeps and must not lose on a restart: run hours, a start count | `state` | `retain: true`, and a `retain:` section |

What each role does at run time:

| | `input` | `output` | `setpoint` | `state` |
| --- | --- | --- | --- | --- |
| **Written by** | the driver, before every scan | the program; the driver sends it to the field after the scan | the operator; the program may too | the program; the operator may too |
| **`POST /api/tags`** | refused with a `400`; [force it](/guides/forcing/) instead | accepted (`204`), but the program's next write to it replaces the value | accepted (`204`) | accepted (`204`) |
| **Built-in dashboard** | read-only | read-only | click to set a number or a BOOL (a STRING, an enumerated value or an array element stays read-only) | click to set a number or a BOOL (a STRING, an enumerated value or an array element stays read-only) |
| **OPC UA client write** | refused, even when `opcua.writable` matches it (`Bad_NotWritable` to a trusted writer session; any other session gets `Bad_UserAccessDenied` first, as for every tag) | allowed when `opcua.writable` opens it to a trusted client | allowed when `opcua.writable` opens it to a trusted client | allowed when `opcua.writable` opens it to a trusted client |
| **[Sparkplug](/guides/sparkplug/#writes) NCMD/DCMD** | refused (logged and counted in `commandsRefused`), whatever `sparkplug.writable` opens | written when `sparkplug.writable` opens it | written when `sparkplug.writable` opens it | written when `sparkplug.writable` opens it |
| **Kept across a restart** (needs a `retain:` section) | never; `retain: true` is a load error | never; `retain: true` is a load error | always | only with `retain: true` |
| **Seeded from `init:`** | only when it has an `init:`, or when a [communications driver](#inputs-from-a-device-that-is-down) delivers it (then the zero of its type); otherwise a read before the first delivery faults the scan | only when it has an `init:`; otherwise it exists from its first write | always: its `init:`, else the zero of its `type:` | always: its `init:`, else the zero of its `type:` |
| **On a standby replica** | not read: a standby does no field I/O | not written: a standby runs no logic | not changed there: writes go to the leader | not changed there: a standby runs no logic |
| **After a failover** | the new leader reads it on its first scan | the new leader writes every output on its first scan | reloaded from the retain store; with no `retain:` section, the new leader's own copy (its seed, on a replica that has never led) | the new leader's own copy (its seed, on a replica that has never led), unless `retain: true` reloads it from the retain store |

A `setpoint` or `state` tag with neither an `init:` nor a `type:` is a
load error (`tag StartLevel: a setpoint needs init or type (its value from
scan one)`). An output the program never writes keeps what the operator
wrote, which is how a [Sparkplug host](/guides/sparkplug-host/) commands
the node it reads. With a `retain:` section, a retained value wins over the
`init:` when the controller starts. On a redundant pair a standby passes
`POST /api/tags` to the leader and answers an OPC UA write
`Bad_InvalidState` ([Redundancy](/guides/redundancy/)).

### One example per role

A lift station, with each role once:

```yaml
retain: {}   # keeps setpoints, and state marked retain: true

tags:
  - { name: Level,      role: input,    type: REAL, unit: "%", desc: "Wet well level" }
  - { name: PumpRun,    role: output,   type: BOOL, init: false, desc: "Pump run command" }
  - { name: StartLevel, role: setpoint, type: REAL, init: 60.0, unit: "%" }
  - { name: StopLevel,  role: setpoint, type: REAL, init: 20.0, unit: "%" }
  - { name: HighLevel,  role: state,    type: BOOL }
  - { name: RunHours,   role: state,    type: REAL, retain: true }
```

```iecst
PROGRAM Main
  PumpRun := (Level > StartLevel OR PumpRun) AND Level > StopLevel;
  HighLevel := Level > 90.0;
  IF PumpRun THEN
    RunHours := RunHours + 0.1 / 3600.0;   (* 100 ms scan *)
  END_IF;
END_PROGRAM
```

- **`Level` is an `input`.** The level transmitter's value, delivered by
  the driver binding that names it ([Modbus](/guides/modbus/),
  [EtherNet/IP](/guides/ethernet-ip/), …). The program only reads it.
  With no `driver:`, the `memory` loopback delivers nothing for `Level`
  and the first scan stops with `undefined tag Level`; give it an `init:`
  to try the example without hardware. To hold it at a value during
  bring-up, [force it](/guides/forcing/). A force needs the tag to exist:
  an input nothing has delivered and no `init:` seeds is refused with
  `400 undefined tag Level`. With an `init:`, or once a communications
  driver has seeded or delivered it, `POST /api/forces` with
  `{"name": "Level", "value": 70}` holds it.
- **`PumpRun` is an `output`.** The program writes it and the driver sends
  it to the starter. The program also reads it back (the seal-in), so it
  has an `init:`; without one, the first scan stops with `undefined tag
  PumpRun`.
- **`StartLevel` and `StopLevel` are `setpoint`s.** The operator tunes
  them: `POST /api/tags` with `{"name": "StartLevel", "value": 65}`, or a
  click in the dashboard. The `retain:` section keeps the 65 across a
  restart.
- **`HighLevel` and `RunHours` are `state`.** The logic owns them.
  `HighLevel` starts at `FALSE`, the zero of its `type:`, and is
  recomputed every scan. `RunHours` is marked `retain: true`, so a restart
  does not reset it to zero.

## How a tag comes to exist

**Every program sees the project's tags without declaring them.** A tag in
`nautilus.yaml` (or a `tag-files:` entry) is in scope in every PROGRAM as
it is — the tag table is the declaration, as a Codesys GVL, a TIA PLC tag
table or Logix controller scope is. A `VAR_EXTERNAL` naming one stays legal,
as IEC's explicit form. Either way the name binds the tag; it does not
create it. Using `TempC` in a program means "resolve this name in the tag
store at scan time", and nothing more. Existence comes from a write, and
there are exactly four writers:

1. a **seed**: the tag's `init:`, or the zero of its type for the roles
   [the table above](#which-role-fits-which-job) seeds without one (it
   exists from scan one),
2. a **driver** delivering it as an input each scan,
3. an **operator** writing a tag the project declares through the HMI or
   `POST /api/tags` (a name nothing declares, a CONSTANT's included, is
   refused with a 400 naming it, never created). Inputs are read-only over
   HTTP: the driver owns them, so a write to a `role: input` tag is refused.
   To drive an input during bring-up, [force it](/guides/forcing/).
4. the **program itself** assigning it (a coil write creates the tag).

The one rule that bites: **writes create, reads fault.** Reading a tag
nobody has ever written stops the scan with `undefined tag <name>` —
deliberately, so a mis-wired binding fails loudly instead of running on a
silent zero. The classic trap is a seal-in latch that *reads its own coil*
on scan one, before the first write; the fix is a seed. (In the FBD
diagram, with live values on, externals with no backing tag are flagged —
an amber *no tag* badge in the vars panel and on any chip that reads one —
so you see this before the download, not after.)

A faulting scan is counted on the diagnostics page (`logicErrors`,
`lastError`) and written to naut's log on transitions, per task: one
warning when a task starts faulting, with the error, and one info line
when it recovers, with how many scans faulted and for how long. While it
keeps faulting, a new error message is logged at most once a minute, so a
flapping fault can't flood the log. A driver read or write that keeps
failing (`ioErrors`, `lastIOError`) is logged the same way.

### Inputs from a device that is down

An `input` delivered by a **communications** driver is the exception: it is
seeded, so a device that is down when the controller starts does not stop
the programs that read its tags. This is how a Logix controller behaves.
It keeps scanning with a consumed tag or a remote rack's inputs at their
last (or initial) value, and the logic checks the connection's fault bit.

| Driver | An input it has not delivered yet… | Status tag to check |
| --- | --- | --- |
| `modbus` | reads its `init:`, or the zero of its type | `<source>__Online` |
| `eip` | reads its `init:`, or the zero of its type | `<driver name>__Online` (`eip__Online`) |
| `sparkplug-host` | reads its `init:`, or the zero of its type | `<site>__Online`, `<site>_<device>__Online` |
| `snmp`, `redfish`, `prometheus` | reads its `init:`, or the zero of its type | `<source>__Online` |
| `plcnext`, `optommp` (local I/O) | faults the scan, as before | `<id>__Online` |
| `replay`, `memory` | faults the scan, as before | — |

The decision is per tag, by the driver that delivers it, so a `drivers:`
list that mixes a Modbus skid with a controller's own I/O seeds only the
Modbus tags. Local I/O keeps the loud fault: there, an input that never
arrives means the configuration is wrong, not that a cable is out. The
fault is the scan's only: the [OPC UA server](/guides/opcua-server/) still
shows such an input from the start, with no value and `Bad_NotConnected`,
so a SCADA client sees the missing point rather than no point.

The type of the zero is the tag's `type:`, else the type the programs
declare the tag as, else the type the driver's binding delivers (a Modbus
binding's format; an EtherNet/IP or Sparkplug binding's datatype). A tag
none of those can type stays absent until its first delivery, and
`naut check` warns about it. No program reads such a tag, since a program
that read it would have declared its type.

A seed is not a reading. Until the device answers, the driver reports the
tag's quality as `notConnected`, which the HMI, the Sparkplug node and the
OPC UA server all carry. Logic should check `__Online`:

```
Run := SkidRun AND Skid__Online;
```

Once a real value arrives it replaces the seed. After a disconnect a tag
holds its last value (quality `stale`), as it always has.

## Tags in scope: the rules

- **A driver binding and its tag are spelled the same.** Programs read tag
  names in any case (`level` binds `Level`), but a driver delivers, and
  accepts writes for, its binding's exact spelling. A tag declared `m1_kw`
  never receives what a Modbus binding named `M1_KW` delivers, so
  `naut check` refuses it: `tag "m1_kw" is spelled "M1_KW" in the driver's
  bindings`.
- **A manifest tag needs no declaration** in a PROGRAM — ST, ladder, FBD or
  SFC. Its type is its `type:`; with no `type:`, the type its `init:`
  implies (`TRUE`/`FALSE` BOOL, a number REAL, `T#5s` TIME, other text
  STRING). A tag with neither has no knowable type, and naming it says so:
  `"Valve" is a project tag with no type — give Valve a type: (or an init:)
  in the manifest, or declare it in VAR_EXTERNAL`.
- **`VAR_EXTERNAL` stays legal** and wins for that program. When the tag
  states a `type:`, the declaration must agree with it — `VAR_EXTERNAL Speed
  : BOOL` against `type: REAL` is an error at the declaration. When the tag
  has only an `init:`, the declaration sets its type (`VAR_EXTERNAL Count :
  DINT` with `init: 0` seeds a DINT), and the `init:` must be a value of
  that type: `init: TRUE` against `VAR_EXTERNAL Speed : REAL` is an error
  (`tag Speed (declared REAL by a program): init: want REAL, got a
  boolean`), as is `init: 2.5` against a DINT. Programs that
  bind one untyped tag with different types (one declares `Count : DINT`,
  another reads `Count` implicitly as the REAL its `init: 0` implies) are an
  error naming both tasks; a `type:` on the tag settles it.
- **A local shadows a tag.** A program's own `VAR Level : REAL;` is that
  program's variable, and the tag `Level` goes unread and unwritten there.
  That is IEC scoping, so it compiles; `naut check` and the editor warn
  (`local Level shadows the project tag Level`).
- **Function blocks and functions do not see tags implicitly.** A block
  that reaches a tag says so in its own `VAR_EXTERNAL`, so it stays
  self-contained and can move to another project. Pass values in through
  `VAR_INPUT` / `VAR_IN_OUT` where you can.
- **A GVL file works too.** A library `.st` holding a file-level
  `VAR_GLOBAL` block (Codesys's `gvl.st`) declares globals for every
  program. A global the manifest does not also declare becomes a `state`
  tag starting at the zero of its type; one it does declare is that tag
  (the manifest supplies its role, init, unit and desc). `VAR_GLOBAL
  CONSTANT` entries are constants, not tags.

A program binds only the tags it names: an unused tag costs a program
nothing per scan.

## A tag's `type:`

`type:` is the tag's data type, the Data Type column of a vendor tag
table. It takes every IEC elementary type — `BOOL`, `SINT` … `ULINT`,
`BYTE` … `LWORD`, `REAL`, `LREAL`, `TIME`, `STRING` — a TYPE the project's
ST declares (a UDT or an enumeration), or an
`ARRAY[lo..hi] OF` either:

```yaml
tags:
  - { name: FT101_Raw, role: input,    type: INT,  init: 0 }
  - { name: PumpRun,   role: output,   type: BOOL }
  - { name: SettleT,   role: setpoint, type: TIME, init: T#2s }
  - { name: Profile,   role: setpoint, type: 'ARRAY[1..4] OF REAL', init: [10.0, 20.0] }
  - { name: P101,      role: state,    type: Motor }
```

`type:` and `init:` must agree: `type: INT` with `init: 2.5` is a load
error naming the tag and its type (`tag N (type INT): init: want INT, got a
number`). A typed `setpoint` or `state` tag with no `init:` starts at the
zero of its type. A type that is none of the above names what the project
does declare (`type Widget is neither an IEC elementary type … nor a TYPE
this project's ST declares (known: DoseRecipe, Motor)`); a function-block
type is refused, since an instance is program state, not a tag. So is a
type that holds one: an array of a block, standard or the project's own,
at any nesting (`tag Timers: ARRAY[1..2] OF TON — TON is a function block;
an instance is program state, not a tag`), and a TYPE whose STRUCT has a
block member (`tag Line1: Station — member Delay is a TON, a function
block; …`). A tag with no `type:` is held to the same rule through the
type a program declares for it: `VAR_EXTERNAL Timers : ARRAY[1..2] OF
TON;` naming a manifest tag is refused at load (`tag Timers: program Main
declares it ARRAY[1..2] OF TON — TON is a function block; …`). An array of
timers belongs in the program's `VAR`; publish the values it produces as
tags.

`GET /api/meta` reports each tag's declared type beside its `desc` and
`unit`, and an enumeration's members, so a client can tell an enumerated
value (which the stream carries as its member's name, `"Run"`) from a
`STRING`. Program locals get the same entry under `locals`; a struct or
function block lists `members` only where an enumeration sits beneath:

```json
"tags": {
  "Mode":  { "desc": "Pump mode", "type": "PumpMode",
             "enum": [{ "name": "Off", "value": 0 }, { "name": "Run", "value": 10 }] },
  "Label": { "type": "STRING" },
  "P101":  { "type": "Pump", "members": { "State": { "type": "PumpMode", "enum": [ … ] } } }
}
```

The VS Code extension reads it to show `Run` bare (and the type, `PumpMode ·
enum`, on hover) where a `STRING` would read `"Run"`, and to offer the
members when you set or force the tag.

## A scalar seed takes the program's type

An `init:` on a plain (untyped) tag is seeded as whatever the programs
declare that tag to be. `init: 0` on a tag the logic binds as
`Counter : DINT` seeds an integer, and the node births it to a Sparkplug
host as an `Int64`; `init: 65` on `TempSP : REAL` is still a REAL (a
`Double` on the wire), so an integer literal is fine wherever a REAL is
meant. A value the declared type cannot hold — `init: 2.5` on an `INT` —
is a load error naming the tag and the type. A tag no program declares has
nothing to resolve against and seeds a number as a REAL, as it always has.

## Seeding struct members

A manifest tag naming a UDT (`type: Motor1Speed`) seeds to the zero of that
type by default — every member `FALSE`/`0`/`0.0` until something writes it.
For a `setpoint` or `state` tag, `init:` can instead be a mapping of member
name to value, nested for a member that is itself a struct:

```yaml
tags:
  - name: ZONE15_FIT_001
    role: state
    type: AnalogInput
    init:
      RAWMIN: 6553.0
      RAWMAX: 32767.0
      HHSP: 2800.0
      ALMDLY: 5
  - name: ZONE15_PMP_015
    role: state
    type: Motor1Speed
    init:
      STRTTMRSP: 30
      LVL: { HSP: 60.0, LSP: 40.0 } # nested struct member
```

A `TIME` member (a recipe's step times) seeds from the ST literal
(`T#2s`, `TIME#1m30s`), the same duration without its prefix (`90s`,
`500ms`), or a whole number of milliseconds. An `ARRAY` member seeds from a
list, element by element from its lower bound; elements the list leaves
out stay zero:

```yaml
  - name: RecipeA
    role: setpoint
    type: DoseRecipe   # STRUCT TargetL : REAL; SettleTime : TIME; Steps : ARRAY[0..2] OF TIME; …
    init: { TargetL: 10.0, SettleTime: T#2s, Steps: [T#1s, 2s] }
```

Every member the mapping omits still takes the zero of its own field type —
`init:` names only what needs a real starting value, not the whole shape.
This is what replaces a first-scan `IF NOT CfgDone THEN ... END_IF` block of
plain field assignments: the same setpoints, declared once where the tag
already is, instead of duplicated as ST in every program that uses the type.

An unknown member name is a load error naming the tag and the member path
(`tag ZONE15_FIT_001: init: unknown member RAWMN (did you mean RAWMIN?)`), and
a struct-typed tag given a scalar `init:` — rather than a mapping — is an
error too. A generator (`naut eip tags`, `naut tags import-csv`)
round-trips the same nested shape through a `tag-files:` entry.

## Writing a member: `POST /api/tags` with `Tag.Member`

A UDT tag is one value in the store, so an operator writing one of its
members is a read-modify-write of the whole struct. The API does that for
you — `name` takes a dotted member path, to any depth:

```sh
curl -X POST localhost:8080/api/tags \
  -d '{"name": "ZONE15_PMP_015.START", "value": true}'
curl -X POST localhost:8080/api/tags \
  -d '{"name": "ZONE15_PMP_015.LVL.HSP", "value": 60}'
```

`value` may also be an object, to set several members of one tag at once:

```json
{ "name": "ZONE15_PMP_015", "value": { "START": true, "LVL": { "HSP": 60 } } }
```

That is a **merge**: members the object doesn't name keep their **current**
values. Deliberately unlike `init:` above, which zero-fills the members its
mapping omits — seeding builds a value from nothing, a write edits one the
plant is already running on.

- **Nothing is created.** An unknown tag, a misspelled member, or a member
  path into a scalar tag is a `400` carrying the reason
  (`tag ZONE15_PMP_015: unknown member STRAT (did you mean START?)`), never a
  new top-level tag literally named `ZONE15_PMP_015.STRAT` — which no program
  reads and a Sparkplug edge would publish as a bogus metric.
- **The leaf keeps its type.** A number lands on a `REAL` member as a REAL
  and on a `DINT` member as an integer; a mismatch is
  `tag ZONE15_PMP_015.START: want BOOL, got a number`, not a silent retype.
- **A value lands exactly as sent, or is refused.** An integer must fit its
  declared width: `70` on an `INT` lands, `70000` is a `400`
  (`tag Int1: 70000 is outside INT (-32768..32767)`), and so is `-1` on a
  `UINT`. A `ULINT` or `LWORD` takes any 64-bit pattern. A fraction for an
  integer (`2.5`), a boolean for a `REAL`, or text for anything but a
  `STRING` or a `TIME` is a `422` on a whole tag (a `400` on a member). A
  `TIME` takes whole milliseconds or its IEC literal, as `init:` and a
  test's `given:` do: `"T#1m30s"`, `"TIME#500ms"`. A string that is no
  duration is the wrong kind of value, like text for a `REAL`. A literal
  finer than a millisecond (`"T#1.5ms"`) or longer than a `TIME` holds is
  a `400`, as `2.5` is: a write is never rounded or wrapped (`init:`
  rounds to the millisecond). The same rules hold for a whole tag, a member, every
  member of an object `value` and every element of a list, and for an
  output no scan has written yet. What the program itself produced is not
  held to them: a retained value comes back after a restart exactly as the
  program left it.
- **An enumerated member takes its name** — `{"name": "Recipe.Mode",
  "value": "Run"}`, any case, `Mode#Run` qualified, or the member's integer —
  and stores the named value, exactly as a write of a whole enumerated tag
  does. A name that is no member is a `400` that lists them
  (`tag Recipe.Mode: "Stop" is not a member of Mode (Idle, Run, Fault)`),
  and so is an integer no member has.
- **An array element is `[n]`**, counted from the array's declared lower
  bound: `Recipe.Steps[2].Mode`, `Line[0].Speed`. An index outside the bounds
  is a `400` that gives them. A list `value` merges onto an array element by
  element (`{"Steps": [{"Mode": "Run"}]}` sets the first step's mode).
- **The role rule is the root tag's**, because the store holds whole tags. A
  member of a driver-owned `input` is refused — the driver replaces the whole
  value before the next scan, so the edit could not survive one cycle. The
  same holds for a whole `input`: it is read-only over HTTP, and a
  [force](/guides/forcing/) is how to drive one during bring-up.
  `setpoint`, `state` and `output` roots are writable; an output is exactly
  how a Sparkplug host commands an edge node, and a struct-typed output needs
  an `init:` so it exists before the first write.
- **`GET /api/meta`** reports `"memberWrites": true`, so an HMI can tell a
  controller that resolves member paths from an older one that swallowed them.
- Every status code `POST /api/tags` answers, and the rest of the API, is in
  the [HTTP API](/guides/http-api/#post-apitags) reference.

The same dotted (and indexed) paths work everywhere else a tag is
addressed: a test manifest's `given:`/`expect:`, a force, the built-in
dashboard's tag table (expand a
writable struct tag and each leaf is editable), and the HMI kit's
`rt.writeTag('ZONE15_PMP_015.START', true)`, which resolves to `null` on
success or the controller's reason when it refuses.

## Observing scans: `Runtime.OnScan`

A Go-level subsystem that needs to react to the tag store every cycle — an
alarm engine evaluating `AI.HH` after each scan, for instance — registers
with `rt.OnScan(func(t *runtime.Tags) { ... })` instead of polling on its
own ticker or wrapping the driver. It fires once per main-task `Scan()`,
after the program has run and any driver outputs have been written, so the
callback sees the store exactly as that scan left it; it also works
unchanged under a test's virtual clock, since it is driven by `Scan()`
itself rather than by wall-clock time. Registered observers run
synchronously, in registration order, and a panic in one is recovered and
logged rather than taking the scan down — but an observer must not block or
write tags, since it runs inside the same lock that serializes every scan.
`OnScan` returns a `cancel` func to unregister; reading a member path from
inside a callback with `Tags.ReadPath("AI.HH")` — the Go-level counterpart
of the dotted paths above, returning a plain scalar for a leaf or an
`ir.Value` for a struct sub-tree — is safe and does not deadlock. See the
doc comments on `runtime.Runtime.OnScan` and `runtime.Tags.ReadPath` for
the full contract.

## Serving the HMI from the controller

`server.hmi` points the controller at a built HMI — a SvelteKit
`adapter-static` build (`npm run build` → `build/`), or any other static SPA
bundle — and it serves that directory at `/` instead of the built-in
dashboard:

```yaml
server:
  hmi: ./hmi/build
```

The path is relative to the project and must resolve inside it, the same
rule as `tag-files:` and `driver.manifest`: what `naut build` ships is
what a reviewer can see in the checkout. An unmatched, non-`/api` path
falls back to the bundle's `index.html`, so client-side routing (SvelteKit's
router, or any other SPA router) resolves a deep link itself instead of
getting a 404 from the controller, which has no idea such a route exists.
The built-in dashboard doesn't disappear — it moves to `/_nautilus/` (its
assets to `/_nautilus/assets/`), so the raw tag table is still one URL away
when you need it. `/api/*` and `/api/stream` are untouched either way.

**The HMI must call the API same-origin** — a relative `/api/...` base URL,
not an absolute host. `server.hmi` and the tag API are one process on one
address, so there is nothing to configure; an app built against a separate
controller URL (the `PUBLIC_NAUTILUS_URL`-style env var some HMI deploys
use for a standalone dev server) should leave that unset, or point it at
`""`, when it's built to be served this way.

`naut build` embeds the HMI's build output in the archive exactly like
every other project file (and prints a warning if the embedded project
comes out over ~50 MB — a sign a dependency tree rode along by accident,
not the HMI build itself); `naut run` serves it straight off disk. Set
it once, and `go build`-style "one file to ship" applies to the operator
screen too.

`build/` is normally gitignored — it's the HMI's own build output, not a
source file — so a fresh clone that hasn't run `npm run build` in the HMI
project yet has no `server.hmi` directory. Neither `naut run` nor `naut
build` fails on that: both warn (`server.hmi: ... is not built yet`) and
fall back to the built-in dashboard at `/` instead of the HMI — `run`
straight off disk, `build` in the binary it ships. Run the HMI's own
build and `run`/`build` again to serve or embed it.

## Adding a field input end to end

Adding a **new field input** to a manifest project is one entry —
`- { name: testExt, role: input, type: REAL, init: 0.0 }` — and the driver
binding that delivers it; every program can then read `testExt` as it is.
In the Go tier it is two more places by the same name: produce it in the
driver (`"testExt": p.testExt` in the `ReadInputs` map) and bind it in the
composition (`runtime.Input("testExt")`; give it a type with
`runtime.Typed` or a seed with `runtime.Init`, or declare it in the
program's `VAR_EXTERNAL`, so the programs know its type). The `Inputs`
list is a deliberate allowlist — a driver can't spray arbitrary names into
the store — which is why the driver map alone isn't enough.

## Declaring tags: one entry per tag

This is the Go form of the manifest's `tags:` list, for an SDK project
(`--template sdk`). The roles are the same four, and
[which role fits which job](#which-role-fits-which-job) applies unchanged:
`runtime.Input` is `role: input`, `runtime.Output` is `output`,
`runtime.Setpoint` is `setpoint` and `runtime.State` is `state`. The
options `runtime.Init`, `runtime.Unit`, `runtime.Desc` and
`runtime.Retained` are `init:`, `unit:`, `desc:` and `retain: true`, and
`runtime.Typed(name, role, type, …)` declares a tag with a `type:`.

`runtime.Options.Tags` states each tag's **role** once — which direction it
moves each scan, its initial value, and its HMI documentation — instead of
spreading the name across the flat `Inputs`/`Outputs`/`Seed`/`Meta` fields
(all of which still work and compose with `Tags`):

```go
Tags: []runtime.TagDef{
    // Field input: the driver produces it. NOT seeded — if the driver
    // doesn't deliver it, you want the loud fault, not a silent zero.
    // (The name must also appear in the driver's ReadInputs map.)
    runtime.Input("TempC", runtime.Desc("Tank temperature"), runtime.Unit("°C")),

    // An input from a device that can be down (Modbus, EtherNet/IP, …):
    // runtime.Comms() seeds it with its Init or the zero of its type, so
    // the program runs until the device answers. The manifest loader sets
    // this for you from the driver.
    runtime.Input("FlowLps", runtime.Comms(), runtime.Unit("L/s")),

    // Operator setpoint / config: seeded so it exists from scan one;
    // the HMI or POST /api/tags writes it later.
    runtime.Setpoint("TempSP", 65.0, runtime.Unit("°C")),

    // Field output: the logic produces it, the driver consumes it.
    // Init() any output the logic also READS — the seal-in latch.
    runtime.Output("PumpRun", runtime.Init(false), runtime.Desc("Pump run command")),

    // Logic-owned state that must survive read-before-write: latches,
    // integrators, mode words.
    runtime.State("Mode", int64(0), runtime.Desc("0=off 1=auto 2=manual")),
},
```

Picking a role in Go, by use case:

| You want a tag that…                        | Use                          | Backed by                                   |
| ------------------------------------------- | ---------------------------- | ------------------------------------------- |
| comes from a sensor / the field             | `Input(name, …)`             | driver `ReadInputs` map + this entry        |
| goes to an actuator / the field             | `Output(name, …)`            | your logic writes it (coil)                 |
| …and your logic also reads it (latch)       | `Output(name, Init(v), …)`   | the seed covers scan one, then the coil     |
| an operator adjusts (setpoint, gain, limit) | `Setpoint(name, initial, …)` | the seed, then HMI / `POST /api/tags`       |
| your logic owns across scans (integrator)   | `State(name, initial, …)`    | the seed, then the coil                     |
| the HMI watches but the field never sees    | plain coil write — no entry  | the program creates it on first write       |

## Performance notes: write generations

The tag store is the seam everything meets at, which also makes it the thing
everything *sweeps*. A controller whose store is 500 UDT tags of 40 members
has 20,000 leaves in it, and the naive version of each consumer walks all of
them on its own clock: the driver copy writes every input every scan, the
Sparkplug edge deep-compares every metric against its last published value
every publish interval, the SSE frame renders the whole store four times a
second, and the runtime hands the driver every output every scan. None of
that is logic. On a 1s-scan controller it dwarfed the logic by two orders of
magnitude.

The store answers all of it with one number. **Every stored value carries
the store generation at which it last CHANGED**, and a write that stores a
value equal to the one already there is not a change: the counter does not
move, and the tag keeps the value it had. The tag store holds values, not
deliveries — re-delivering the same reading is not an event.

That makes "has this changed since I last looked?" an integer comparison
rather than a walk:

| Consumer | Question | What it compares |
| --- | --- | --- |
| `Scan()` output push | did any output move? | `Tags` output generation vs the last successful `WriteOutputs` |
| Sparkplug RBE | did this metric move? | the metric's `Sample.Gen` vs the generation it was last published at |
| SSE frame / HMI | did anything move? | `Tags.Generation()` vs the last rendered frame |
| Anything keyed by tag NAME | did a tag appear? | `Tags.NameGeneration()` |

The public surface is four methods on `runtime.Tags` — `Generation()`,
`NameGeneration()`, `TagGeneration(name)`, and `SnapshotInto(dst)`, which
refills your map with `Sample{Value, Gen}` instead of allocating a fresh
whole-store copy per tick. `Snapshot()` and `All()` are unchanged; prefer
the generation-aware forms in anything that runs on a clock.

Two consequences are worth knowing about:

**`WriteOutputs` is a push of changes, not a per-scan heartbeat.** The
runtime calls it on the first scan, on any scan where an output tag's value
differs from the one it last handed over, after a failed write, and after a
redundancy takeover — and skips it otherwise. Every driver in tree already
reduced the call to that internally; now the call itself is skipped. A
driver that genuinely needs a per-scan call — a watchdog to re-arm, a bus
whose outputs decay without a rewrite — asks for one with
`runtime.Options{AlwaysWriteOutputs: true}`.

**A driver can skip the per-scan input map.** `ReadInputs` returns a fresh
`io.Values` every scan; on a driver with thousands of bindings that is tens
of kilobytes of garbage per scan for a map whose keys never change. A driver
may also implement `io.BatchReader` — `ReadInputsInto(dst io.Values) error`
— and the runtime hands it the same map back each scan. It is never
required, and the runtime falls back to `ReadInputs` when it is absent.

The change detection is deliberately conservative: two struct values are
equal only if they share the same `StructDef`, and a function-block instance
is never equal to anything (an FB is identity — its retained frame — not a
value). The only mistake it can make is to report a change that did not
happen, never to hide one.

The same counter is what makes an SSE **delta stream** possible: a client's
entire subscription state is one `uint64`, and "what changed since you last
heard from us" is an integer comparison per tag with no value comparison
anywhere. `Tags.ChangedSince(gen, dst)` appends exactly the tags that moved
and hands back the generation to ask with next time. See
[Streaming and data quality](/guides/streaming/).

## What a value is worth: quality

The store holds values, not statements about values. Whether a reading is
current, held over from a source that went away, or from a device that has
never answered is not a property of the number — it comes from the driver,
and it travels beside the tags rather than inside them.

A driver may implement `io.QualityReporter`; the runtime adds its own
derivation (a driver-bound input whose last read failed is stale); and the
server puts the non-`good` entries on `/api/state` and every stream frame
as a `quality` map. An absent name means `good`, so a healthy plant pays
nothing for it. The full model — the four values, where each comes from,
and why an HMI must check `/api/meta`'s capability flag before drawing a
quality badge — is in
[Streaming and data quality](/guides/streaming/#per-tag-quality).
