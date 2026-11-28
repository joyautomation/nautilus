# Design: one struct model (types and wiring)

**Context:** a new context, *Struct wiring* (§16), between the Manifest and
the field and IT drivers. It changes the Runtime (members with their own
kinds, §2.3) and the Device library (profiles produce wiring, §12). Driver
names stop at the driver packages, as now. Core: the type a program sees is
the product. Issue #1458; related #717. Status: **proposal**, for naut-lead
and James. No code until the brief is approved.

## 0. Why, and what is decided

nautilus has two struct models today, and a user has to pick between them.

- A **user `TYPE`** is plain IEC. It can be nested, hold arrays and be an FB
  pin. Its tags get no Modbus or OPC UA I/O per member (only scalar
  bindings), and it has no computed members. EtherNet/IP and the Sparkplug
  host fill a whole one from a peer.
- A **device profile type** (`devices:` rows, #717) gets I/O per member,
  derived BOOLs and alarms. But commands are separate scalar tags
  (`PUMP_ControlWord`), every device of one type must have the same members,
  there is no per-instance override and no include file, and Logix and
  Siemens targets refuse it.

The goal (#1458, product direction): **one structure, like a SCADA UDT.**
Each member of a type is memory, I/O-bound (its address set per instance),
or an expression. A type has per-instance parameters and overrides, alarms,
and read-backs and commands on one instance that an FB can take as a pin.

**Decided on #1458** (James, in the issue comments; quoted where it matters):

1. **One unified type.** It is a plain IEC `TYPE` with **wiring** beside it,
   never inside it.
2. **Mixed sources.** One type may take members from several drivers.
3. **Where the wiring lives:** in `nautilus.yaml` or in a separate file, as
   the user chooses.
4. **Calculated members** are an ST expression. It may call a user ST
   `FUNCTION`, and the checker refuses a function with side effects.
5. **Memory members** are retained or initial, set per member. **Retained
   is the default.** This brief defines what a redeploy does, and how a
   redundant pair replicates the values.
6. **A type a driver fills whole can take user members**, and the driver
   reads and writes only the device's own members. This brief picks extend
   or composition.
7. **Writability is decided per member in the wiring:** `from:` is never
   writable, `to:` is writable (sent on change), `memory:` is writable, and
   `expr:` is never writable. Writing a `from:` or `expr:` member from ST,
   the HMI or an NCMD is a check-time error.
8. **`Cmd` is a convention, not a keyword.** Profiles and importers put
   generated commands there. The user may name the group anything, and
   `naut check` warns about neither.
9. **`readback:`** on a `to:` member names its read-back.
10. **`both:`** (read and write the same member) is allowed only on
    peer-controller drivers. Field-device drivers refuse it.
11. **Sparkplug B Templates work both ways.** This covers publishing,
    commands, parameters, version, the host side and a round trip tested in
    CI.
12. Who may write (auth) is a separate check on top of the wiring.

**What must not break** (from the issue): a member shows the device's value,
never the program's intent. No command is replayed after a write gate
reopens. `hw.WriteDecl` is shared with the IT drivers. The `TYPE` stays plain
IEC, so the writers keep writing it.

The 11-17 decision on #717, "derive at load, from a vendored copy", still
holds. Nothing in this brief generates files the user then edits (§12).

## 1. Type and wiring

Three nouns:

- **type**: a plain IEC `TYPE ... STRUCT`, written in the project's ST like
  any other. Member initial values (`SpeedSP : REAL := 50.0;`) are IEC and
  are used (§7).
- **wiring**: YAML beside the type. For each member it says where the
  value comes from. It names its **sources** (driver connections) and its
  **params**. One type may have several wirings, for example the same pump
  on Modbus at one site and on OPC UA at another.
- **instance**: a tag of a wired type. It names the wiring and gives the
  params and the overrides.

```iecst
TYPE PumpCmd : STRUCT
  Start    : BOOL;
  Stop     : BOOL;
  SpeedRef : REAL;
END_STRUCT; END_TYPE

TYPE Pump : STRUCT
  Running  : BOOL;
  Speed    : REAL;          (* % *)
  Fault    : BOOL;
  Healthy  : BOOL;
  SpeedSP  : REAL := 50.0;
  RunHours : REAL;
  Cmd      : PumpCmd;
END_STRUCT; END_TYPE
```

```yaml
# nautilus.yaml, or a file listed in wiring-files: (§13)
wirings:
  - type: Pump                       # the wiring's name defaults to the type's
    params: { endpoint: null, unit-id: 1 }   # null: every instance must give it
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: "{unit-id}" }
    members:
      Running:      { from: drive, table: coil, address: 0 }
      Fault:        { from: drive, table: discrete, address: 2 }
      Speed:        { from: drive, table: holding, address: 4, format: int16, scale: 0.1, unit: "%" }
      Healthy:      { expr: "Running AND NOT Fault" }
      RunHours:     { memory: retained }
      Cmd.Start:    { to: drive, table: coil, address: 10 }
      Cmd.Stop:     { to: drive, table: coil, address: 11 }
      Cmd.SpeedRef: { to: drive, table: holding, address: 20, format: int16, scale: 0.1, readback: Speed }
    alarms:
      - { member: Fault, name: "{desc} fault", priority: high, class: pump }

instances:
  - { name: P101, type: Pump, desc: "Lift pump 1", params: { endpoint: 10.0.0.11 } }
  - { name: P102, type: Pump, desc: "Lift pump 2", params: { endpoint: 10.0.0.12 }, set: { SpeedSP: 40.0 } }
```

- **A member's address keys are its driver's own manifest columns**
  (`table`, `address`, `format`, `scale` for Modbus; `node` for OPC UA;
  `tag` for EtherNet/IP). Device profile points already reuse them. The
  wiring layer hands each driver the manifest its own file would decode to,
  so the drivers do not change, as with `devbind` today.
- **A member the wiring does not name is memory, retained** (decision 5).
  In the example, `SpeedSP` is one. A name the wiring gives that the type
  does not have is an error with a did-you-mean, so a typo cannot turn a
  device member into memory without a word. (Q2)
- **Nested paths** (`Cmd.Start`) wire one leaf. Naming a struct member
  (`Cmd: { memory: initial }`) gives every leaf beneath it that kind, and a
  leaf entry below it wins.
- **Arrays.** A member that is an array is wired whole (one binding that
  delivers the array) or element by element (`Levels[1]`). Arrays of
  structs are wired element by element. (A driver that cannot deliver an
  array refuses it, as now.)
- **A source** names a `drivers:` entry by name, or a driver type when the
  project has exactly one of it (`modbus`). For a protocol where one driver
  holds many connections (Modbus, SNMP, Redfish, Prometheus), the source's
  keys (`endpoint`, `unit-id`) become one connection inside that driver for
  each distinct value. For a protocol where one driver is one connection
  (OPC UA client, EtherNet/IP, Sparkplug host), a source with its own
  `endpoint` makes one driver per instance. That driver is named after the
  instance, and the entry it names is a template section, exactly as a
  `devices:` row's `driver:` is today (device-library.md §2). A source with
  no `endpoint` shares that entry's one connection among all instances.
- **The type stays plain IEC.** Nothing about I/O, kinds or params appears
  in ST. The Logix and Siemens writers still see only the `TYPE`.

## 2. Member kinds and writability

### 2.1 The five kinds

| Kind | Value comes from | Writable by |
|---|---|---|
| `from: <source>` | the device, each poll | nobody: a check-time error from ST, a `400` from the API, a refusal from OPC UA and Sparkplug |
| `to: <source>` | the program (or an operator); the driver sends it on change | the program, an operator, a client the server opens it to |
| `memory: retained` or `initial` | its initial value, then whoever writes it | the program, an operator, a client the server opens it to |
| `expr: "<ST>"` | evaluated once per scan, before programs (§6) | nobody, as for `from:` |
| `both: <source>` | the peer's own memory, read each poll; writes go to the peer | the program, an operator, a client the server opens it to; **peer drivers only** (EtherNet/IP to a Logix controller, the Sparkplug host) |

"A client the server opens it to" means each server's existing opt-in,
`opcua.writable` and `sparkplug.writable`, and on top of that `auth:`. The
wiring is an upper bound. It never opens a write that those settings keep
shut (decision 12).

`both:` on a field-device driver (Modbus, OPC UA client, SNMP, Redfish,
Prometheus) is refused: a register belongs to the device, so writing one
and reading the same value back as truth would hide whether the device took
it. Use a `to:` with a `readback:` instead.

### 2.2 How each kind behaves, in the tag model's words

This extends the tag-model guide's role table to members. The instance tag
itself has no `role:`, and `/api/meta` reports it as `instance` with each
member's kind.

| | `from:` | `to:` | `memory: retained` | `memory: initial` | `expr:` | `both:` |
|---|---|---|---|---|---|---|
| **Like the role** | `input` | `output` | `setpoint` | `state` | (new) | `input` + `output` on one member |
| **First scan** | its initial value, else zero, quality `notConnected` until delivered (every wiring source is a communications driver) | its initial value, else zero; not handed to the driver until something writes it (as a typed output) | the retained value, else `set:`, else its initial value, else zero | `set:`, else its initial value, else zero | evaluated on the first scan, `notConnected` until its inputs arrive | as `from:` |
| **Kept across a restart** | never | never: a restart must not replay a command | yes (with a `retain:` section, Q3) | never | never | never |
| **On a standby** | not read (stale) | not written | not changed there; writes go to the leader | not changed there | not evaluated (stale), since a standby runs no logic | not read, not written |
| **After a failover** | read on the new leader's first scan | the driver's own takeover rule, as for an output today | reloaded from the retain store or the streamed image | the new leader's own copy, as for a `state` tag | evaluated on the new leader's first scan | as `from:`, and as `to:` |

### 2.3 What the runtime does

Today the store holds whole tags, the driver replaces a whole input before
each scan, and the role belongs to the root tag (`runtime/runtime.go`
`setMany`). The instance needs four changes, all in the Runtime context. The
manifest hands them in as runtime types, so the Runtime never imports the
wiring.

1. **Member kinds.** `runtime.TagDef` gets the kind of each member path of
   an instance (additive, since `runtime` is stable Go API). The write paths
   (`POST /api/tags`, forces, the OPC UA server, Sparkplug commands, a
   test's `given:`) check the path against it. A whole-tag write to an
   instance is a merge that may touch only writable members. One that names
   a `from:` or `expr:` member is refused whole, naming the member.
2. **Partial delivery.** The wiring adapter delivers each instance's
   `from:` and `both:` members as a partial value. The store merges it with
   the existing write-mask machinery (`runtime/membermerge.go`), so `memory:`
   and `to:` members are never touched by a delivery. The drivers still see
   only flat bindings under internal names, as `devbind`'s adapter does.
3. **The expression stage** (§6) runs between the input read and the
   programs, in the main task's scan.
4. **The output push** hands the driver the `to:` and `both:` leaves that
   changed, under their internal binding names, with each driver's own
   first-value rule and gate mode (below).

The scan becomes: read inputs, merge `from:`/`both:`, evaluate `expr:`, run
the programs, commit, then push the changed `to:`/`both:` leaves.

**Each driver keeps its own write rules.** A `to:` member is that driver's
output, so it keeps the driver's first-value rule and gate mode. Modbus,
OPC UA and EtherNet/IP compare the first value with what the device holds.
The Sparkplug host takes it as a baseline and sends nothing. For the gate,
the IT drivers use `dropAndRebaseline` and the Sparkplug host
`holdAndDeliver`. The driver conformance suite gets a wired-instance row per
driver, so the adapter cannot quietly change a mode.

**Quality is per member.** Each leaf's quality is its binding's quality.
An `expr:` member takes the worst of its inputs. `memory:` and `to:` are
good. The root tag is the worst of its `from:`, `both:` and `expr:` members,
which is the device library's per-device rule, kept. The server publishes
the root and an entry for each member path whose quality is not good. The
HMI kit already prefers an exact path entry over the root's ("an exact entry
for the full path still wins", streaming guide), so screens get member
quality without a kit change. (Q6)

### 2.4 Functions and function blocks taking an instance

The headline use is an FB that takes the instance as a pin.

- **`VAR_INPUT`** copies the instance in. Nothing is written back.
- **`VAR_IN_OUT`**: the tag model counts every member as assigned on every
  call, because the call copies the pin back. For an instance:
  - `from:` and `expr:` leaves are **dropped from the write mask** at
    commit. They are never the program's, and the check (below) refuses a
    real assignment, so copy-back cannot write a stale value over a
    delivery.
  - `to:` and `both:` leaves copy back the value they had unless the FB
    assigned them. An equal value is not a change, so no command goes out.
  - `memory:` leaves follow today's rule: copy-back wins over an operator
    write that landed during that scan. This is documented, and Q7 asks
    whether to make it exact.
- **The check works at the call site.** The checker already follows writes
  through the blocks and functions a program calls (the "a program's own
  earlier write counts" analysis). A call that passes an instance to a
  block that assigns a `from:` or `expr:` member is an error at the call,
  naming the block's line. The same block called with a plain local struct
  of that type is fine, so one FB serves both.
- **A whole assignment** to an instance (`P101 := Spare;`, an FB's
  `VAR_OUTPUT` of the type assigned to an instance) is refused when the
  type has any `from:` or `expr:` member.
- **One kind per member per type.** Every wiring of a type must give each
  member the same kind. A per-instance override may change an address,
  never a kind (§4). This is what makes the call-site check sound: a
  member's writability depends on its type, not on which wiring an
  instance uses.

## 3. Mixed drivers in one type

Each member names its source, and a wiring may have any number of sources
on any drivers:

```yaml
wirings:
  - type: Tank
    params: { area: null, n: null }
    sources:
      level: { driver: plant-ua }                    # one OPC UA server for all tanks
      pump:  { driver: modbus, endpoint: "{pump}" }  # a drive per tank
    members:
      Level:     { from: level, node: "ns=2;s={area}.LT{n}.PV", unit: "%" }
      PumpOn:    { from: pump, table: coil, address: 0 }
      Cmd.Run:   { to: pump, table: coil, address: 10, readback: PumpOn }
      HighLevel: { expr: "Level > HighSP" }
      HighSP:    { memory: retained }
```

- **Each source is its own connection** with its own write gate, its own
  health and its own `__Online`. There is no atomic command across sources:
  two `to:` members on two sources changed in one scan are two independent
  writes, in no guaranteed order. A sequence that needs order writes one,
  waits for its read-back, then writes the next.
- **Companion tags.** An instance with one source gets `<Instance>__Online`
  (and `__Outages` for Modbus), as a `devices:` row does. With several
  sources, each source gets `<Instance>_<source>__Online`, and
  `<Instance>__Online` is true only while every source answers. (Q8)
- **Alarms are suppressed per source.** A member's alarm carries the
  `enable:` of its own source's `__Online`, and each source raises its own
  comms-loss alarm (§5).
- **Sparkplug.** An instance whose `from:`/`both:` members all come from one
  connection rides that connection's device, as a `devices:` row's struct
  tag does. A mixed instance publishes at node level. (Q9)

## 4. Per-instance params and overrides

**Params.** A wiring declares `params:` with defaults. `null` means each
instance must give it. A value is substituted as `{name}` into source keys
and member address keys. This is `devprofile`'s existing substitution
(`Profile.Substitute`), so `{n}`, `{area}` and `{endpoint}` already work the
same way in profiles. Two params come built in: `{name}` (the instance's
name) and `{desc}`. There is no arithmetic in a substitution (§17). A
substituted address that does not parse for its driver is an error naming
the instance, the member and the value.

**Instance keys:**

| Key | Meaning |
|---|---|
| `name` | the tag name (an IEC identifier) |
| `type` | the type |
| `wiring` | which wiring, when the type has more than one (the default is the one named after the type) |
| `params` | values for the wiring's params |
| `set` | initial values for `memory:` members by path, nested like a tag's `init:` (`set: { SpeedSP: 40.0, Limits: { Hi: 90 } }`); only memory members, or it is an error naming the kind |
| `wire` | per-member wiring overrides: another address or source for this instance only (`wire: { Speed: { address: 6 } }`), merged over the wiring's entry; it may not change the kind |
| `alarms` | per-instance alarm tuning by member: `{ Fault: { priority: low } }`, or `false` to turn one off (§5) |
| `offline-alarm` | per source, as a `devices:` row's (§5) |
| `desc` | one line for the tag, the faceplate and the Sparkplug description |

There is no `points:`-style selection on an instance. A type's members are
fixed, so an instance has them all (§12 covers how profiles keep selection).

## 5. Alarms in the type

A wiring's `alarms:` names BOOL members of any kind (`from:`, `expr:`,
`memory:`), with the alarm fields a profile alarm takes (`name`,
`priority`, `on-delay`, `class`, `notes`). They render exactly as profile
alarms do today (device-library.md §3.4), so these rules carry over:

- one rule per wiring member, matched over the instances of that wiring;
- the project's own `alarms:` win (first match), and a duplicate id is the
  ordinary error naming both sources;
- **no `alarms:` section, no alarms**, and `naut check` warns;
- the tuning overlay (#1249) reaches every rule by id, so per-instance
  `alarms:` on an instance is a shorthand that renders into that overlay
  rather than a second mechanism;
- equipment out of service (#715) covers an instance's alarms, and the
  equipment is the instance's name.

**Suppression and comms loss**, generalized from the device library:

- each rule carries `enable:` set to its member's source's `__Online`.
  An `expr:` member's rule carries the AND of the sources it reads.
  A `memory:` member's rule has none;
- each source of each instance raises a **comms-loss alarm**
  (`<Instance>__Offline`, or `<Instance>_<source>__Offline` with several
  sources). It fails active and has the same defaults (high, class `comms`,
  5 s on-delay), tuned or turned off by the instance's `offline-alarm:`.

`{desc}`, `{name}` and the params substitute into an alarm's `name` and
`notes`.

## 6. Expression members

`expr:` is one ST expression:

```yaml
Healthy:  { expr: "Running AND NOT Fault" }
Tripped:  { expr: "StatusWord.3" }               # a profile's derived bit
SensorBk: { expr: "Loop1Error = 39" }            # a profile's derived eq
FlowM3h:  { expr: "FlowFromDp(DiffPress, {k})" } # a user FUNCTION, a param
```

- **Scope.** A bare name resolves to a sibling member first (`Cmd.Start`
  for a nested one), then to a project tag. A sibling that shadows a tag is
  a warning, as a local is today. Params substitute as text before compiling
  (`{k}`), so each distinct set of values compiles once. The expression is
  read-only: it is an expression, so it cannot assign anything.
- **Type.** The result must be assignable to the member's type without
  narrowing, or it is a check error naming both types.
- **Functions.** It may call the standard functions and the project's own
  `FUNCTION`s. The checker refuses a function, at any depth of calls, that:
  - declares `VAR_EXTERNAL` (writing a global is a side effect, and reading
    one is an input the scheduler and the quality rule cannot see);
  - has a `VAR_IN_OUT` parameter;
  - declares or calls a function block instance;
  - calls a system function with an effect (channel `READ`/`WRITE`, and the
    redundancy and alarm functions);
  - calls a refused function.

  The error names the function, the line and the rule, and says to make it
  a program or a block instead.
- **When it runs.** It runs once per scan of the main task, after the input
  merge and before any program, in dependency order (an `expr:` reading
  another `expr:`). A cycle is a check error naming every member in it.
  Programs in other tasks see the value the last main scan committed.
- **Quality** is the worst of everything it read.
- **Until its inputs arrive** it is `notConnected`, and a BOOL is false (the
  device library's "false until the source is delivered" rule, kept). **On
  comms loss** it is recomputed from the held inputs, with their `stale`
  quality (the "holds on comms loss" rule, kept).
- **A run-time fault** (division by zero, an index out of range) leaves the
  member at its last value with quality `bad`. It is logged when it starts
  and when it clears, as a task fault is, and the programs still run.
- **Cost.** Expressions compile once to the VM and evaluate with no
  allocation (the real-time rule). The wasm build carries the stage, and
  the wasm size budget in CI covers it.

There are no timers, edges or state in an expression. Those belong in a
program or a block (§17).

## 7. Memory members: retained or initial

**The initial value** is, in order: the instance's `set:`, then the
member's initial value in the `TYPE`, then the zero of its type.

**`memory: retained`** (the default) keeps the value in the retain store,
beside setpoints and `retain: true` tags. Only the retained memory members
of an instance are saved. Its `from:`, `to:`, `expr:` and initial members
are not. The retain store already brings a struct back member by member onto
the current tag, so this needs no new format.

**What a redeploy does** (decision 5 asks for this):

- **The retained value wins** over a changed `set:` or a changed `TYPE`
  initial value, as a retained setpoint's value wins over a changed
  `init:` today. What the operator set last outlives what the file said
  first.
- **The difference is shown, not lost.** A scalar numeric or BOOL retained
  memory member with a literal initial value is a **parameter**, named by
  its path (`P102.SpeedSP`), in the parameters workflow
  (`naut params diff`, `export`, `import`, write-back). After a redeploy
  that changed its `set:` it shows **repo-ahead**. An operator's edit shows
  **plant-ahead**, both moved is **conflict**, and otherwise it is
  **in-step**. `naut params import` applies the repository's values on
  purpose, all or nothing. Parameters today are top-level setpoint tags
  only, so this extends them to member paths.
- **A new member** starts at its initial value. **A removed member's**
  retained value is dropped and logged. **A member whose type changed**,
  or that stopped being retained memory, has its retained value discarded,
  starts at its initial value, and is logged and named in the retain
  diagnostics. (Q5)

**`memory: initial`** starts from its initial value on every start and on
every cold takeover, as a `state` tag without `retain: true` does today.

**A redundant pair** replicates retained memory members to the standby the
way it replicates any retained value. A quorum pair carries them in the
leader's streamed image, and the standby writes them to its own retain file.
A Kubernetes pair keeps them in the ConfigMap. A takeover therefore keeps
them. A test for this goes in the redundancy suite: write a retained member
on the leader, take over, read it on the new leader.

**With no `retain:` section**, nothing is retained. A retained memory
member then starts from its initial value on every restart, as a setpoint
does, and `naut check` warns once, naming the instances. (Q3)

## 8. Imported types: extend (preferred) or wrap

A **fill driver** fills a type whole from a peer:

- EtherNet/IP, from a Logix UDT;
- the Sparkplug host, from a Template;
- the IT drivers, into the shared `hw` UDTs;
- later, at low priority, an OPC UA structure (server structures vary).

The wiring says so with `fill:`. The driver reads, and for `both:` members
writes, only the members that came from the device:

```yaml
wirings:
  - type: Motor                    # extends LogixMotor, the imported type
    params: { tag: null }
    sources: { plc: { driver: line-plc } }
    fill: { from: plc, tag: "{tag}" }            # every LogixMotor member is from: plc
    members:
      SpeedSP:  { both: plc }                    # a peer member the program may write
      Healthy:  { expr: "NOT Faulted AND Running" }
      RunHours: { memory: retained }
```

**Extend** (recommended) keeps paths flat (`M1.Speed`, `M1.RunHours`):

```iecst
TYPE Motor : STRUCT EXTENDS LogixMotor
  Healthy  : BOOL;
  RunHours : REAL;
END_STRUCT; END_TYPE
```

**Wrap** is pure IEC and one level deeper (`M1.Plc.Speed`). It works today,
with no language change, and stays supported:

```iecst
TYPE Motor : STRUCT
  Plc      : LogixMotor;
  RunHours : REAL;
END_STRUCT; END_TYPE
```

Its wiring says `fill: { member: Plc, from: plc, tag: "{tag}" }`.

Extend is preferred because the imported type's members are the paths
people already know from the peer, and a re-import does not move them.

**`STRUCT EXTENDS` is a language change of its own** (slice 8). Nothing in
`lang/` parses `EXTENDS` today, not even for function blocks. IEC
61131-3 defines `EXTENDS` only for function blocks and classes. The struct
form is spelled the way CODESYS spells it, so it reads as familiar, but it
is a nautilus extension, and the brief says so (Q20). It is designed so that
nothing past the front end ever sees it:

- the parser and checker take one base, which must be a `STRUCT`. The base
  may itself extend another. A member name that repeats a base member, and
  a cycle, are errors naming both;
- `lang/ir` **flattens at compile**: base members first, in order, then
  the new ones. The `StructDef` records the base name for tools. The
  runtime, retain, Sparkplug, the OPC UA server, `/api/meta` and the
  writers only ever see a flat struct;
- the LSP gives hover, completion and go-to-definition through the base,
  `lang/stgen` writes `EXTENDS` back, and conformance cases cover it;
- **the writers flatten it**: Logix UDTs and Siemens UDTs have no
  inheritance, so they get the flat member list.

**Re-import.** `naut eip import` or `naut sparkplug import` regenerates
the base type, and the extension in the user's own file is untouched. A new
base member that collides with a user member is a check error naming both,
and the fix is to rename the user's member.

**Writes in a fill.** A fill's members are `from:` unless the wiring makes
one `both:`. EtherNet/IP already writes a struct member by member,
symbolically, sending only what changed. The Sparkplug host already sends
partial templates. Both keep their rules: for EtherNet/IP the first value is
compared with what the controller holds, and for the host it is a baseline.

**A `both:` member while its write is in flight.** Without a rule, the
next poll can deliver the peer's old value before the write lands, and the
store flickers back. The adapter holds a `both:` member at the written
value until the driver answers that write (EtherNet/IP) or the peer's next
data carries the member (Sparkplug), up to a bound of a few poll periods.
After that, delivery is the truth again. (Q13)

Two writers on one `both:` member (the program and another client of the
peer) fight, as two writers to one Logix tag do. The tag-model advice holds:
assign it only when the logic means to change it.

## 9. `Cmd`, the generated convention

`Cmd` is where generators put the `to:` members they make:

- **device profiles**: each writable point `X` becomes `Cmd.X`, a `to:`
  with `readback: X` (§12);
- **IT importers** (`naut snmp|redfish|prometheus import`): each
  `writes:` command, today the scalar `<tag>_Cmd` tag, becomes a `Cmd`
  member. `hw.WriteDecl` stays the driver-side mechanism: the wiring layer
  turns a `to:` member into a `WriteDecl` whose `Name` is the internal
  binding name. Its `Kind` (an INT power command against a BOOL `PowerOn`)
  and its `dropAndRebaseline` gate mode are kept. The IT drivers do not
  change. The shared `hw` UDTs gain `Cmd` through an extension the import
  generates, not by editing the published UDTs.

The reason behind the 11-17 ruling is kept. A `from:` member still shows
the device's value, never the program's intent. The intent lives only in
`to:` members, under a name that says so. The shape changes from a
separate tag to a member of the same instance, so an FB pin carries both.

A user may put `to:` members anywhere and name the group anything.
`naut check` warns about neither (decision 8). A type member literally
named `Cmd` that is not a struct is fine. A generator that needs `Cmd` and
finds a user member there refuses, naming the member.

**Peer importers (EtherNet/IP, Sparkplug host) are the exception**, and
this differs from the 11-28 comment's wording, so it is Q12. A peer's
writable member is the peer's own memory. It is a `both:` member where the
peer has it (`Pump1.Speed`), not a generated `Cmd.Speed`. Moving it into a
`Cmd` that the peer's type does not have would mean the imported type is no
longer the peer's type, and that breaks the Sparkplug round trip (§11).

## 10. `readback:`

`readback: <path>` on a `to:` member names the member that shows what the
device did with it: `Cmd.SpeedRef: { to: drive, ..., readback: Speed }`.

- It must name a `from:`, `both:` or `expr:` member of the same instance.
  The types may differ: an INT power command reads back as a BOOL
  `PowerOn`.
- `/api/meta` reports it for each member. The faceplate and the HMI kit
  show asked against actual from it, and the device library's asset page
  uses it to put a setpoint beside its reading.
- **"Commanded, not reached"** is an `expr:` plus an alarm:
  `SpeedOff: { expr: "ABS(Cmd.SpeedRef - Speed) > 2.0" }`, with an alarm
  that has an on-delay. A shorthand that generates the pair is Q16.

## 11. Sparkplug B Templates, both ways

**As built today:** the edge publishes every UDT tag as a Template, with
the definition in the NBIRTH and one instance per tag. `flatten-udts`
exists for hosts that skip Templates. The definition carries no `version`
(`sparkplug/templates.go`), and the codec's `Template` has no parameters.
The edge publishes every integer as Int64 and every real as Double, and the
host imports them as `LINT` and `LREAL`.

### 11.1 Publishing (the edge)

- **One type is one Template definition**, holding every member of every
  kind (device, memory, expression, `Cmd`) in `TYPE` order. A nested struct
  is a nested Template (as now). `EXTENDS` is flattened into the
  definition, since Sparkplug has no inheritance and `lang/ir` has already
  flattened it.
- **Version.** The definition's `version` is a short hash of what defines
  it: the member names, types and order, and the param names. A change a
  host must notice changes it, and nobody has to remember to bump it. A
  wiring may set `version:` explicitly, which an imported type does in
  order to keep the version its source published. (Q10)
- **Parameters.** The wiring's `params:` become the definition's Template
  parameters, with their defaults. Each instance carries its own values, the
  way SCADA UDT parameters map onto them. This needs the parameters field in
  the payload codec (`sparkplug/spb` and `payload.go`).
- **Member properties.** Each definition member carries a small property
  set:
  - `kind` (`from`, `to`, `memory`, `expr`, `both`);
  - the IEC type when Int64 or Double loses it (`INT`, `REAL`, `TIME`, an
    enumeration's name with its members);
  - an array's bounds.

  Hosts that do not read properties ignore them, and the wire datatypes do
  not change. (Q11)
- **Commands.** An NCMD or DCMD may target only `to:`, `both:` and
  `memory:` members, and only where `sparkplug.writable` opens them and
  `auth:` grants it. A command to a `from:` or `expr:` member is refused,
  logged once per tag and counted in `commandsRefused`, as a command to an
  input is now. The no-replay rules hold, because the command writes the
  store and the push follows the driver's own gate mode (§2.3).
- **Device or node.** See §3: an instance with one connection rides that
  connection's device, so its `Cmd` members make a DCMD useful for the
  first time.
- **`flatten-udts: true`** publishes members as metrics, as now
  (`P101/Cmd/Start`). There are no definitions in that mode, so there are
  no parameters or version either, and the documentation says so.

### 11.2 The host side

- **The Sparkplug host is a fill driver** (§8): a wiring with
  `fill: { from: site, node: W6, metric: Pump1 }` over the imported type.
  User members come by extend (preferred) or wrap.
- **Writes stay partial templates.** A `both:` member replaces today's
  per-member scalar output tag (`W6_Pump1_Speed`), which was there only
  because a member could not be written in place. The old shape keeps
  working (§12's migration rules apply to it too). The importer's
  `--writable` still decides which members are `both:`. Nothing opens by
  default. A definition member whose `kind` property says `from` or `expr`
  is refused by `--writable`, naming it.
- **Parameters** in a definition become the generated wiring's params, with
  each instance's values filled in, shown in `/api/meta`.

### 11.3 The round trip, tested in CI

A type published by one nautilus and imported by another comes back as the
same TYPE: the same name, members, order and member types. The test is an
acceptance case over the in-process broker (`internal/testmqtt`):

1. Project A has types covering every integer width, REAL and LREAL, TIME,
   an enumeration, a nested struct, an array with a non-zero lower bound,
   an `EXTENDS`, a `Cmd` group, and memory and expression members.
2. A publishes. `naut sparkplug import` runs against the births.
3. The generated `TYPE` text equals A's flattened `TYPE` text, after
   formatting, and the versions and params match.

The member properties of §11.1 are what make this possible. Without them,
`INT` comes back as `LINT`.

## 12. Device profiles as a producer of type and wiring

A profile plus a `devices:` row means exactly a type plus a wiring plus an
instance. `devbind` stops being a second model and becomes a **producer of
wiring**. It is derived at load, in memory, from the vendored copy, as now
(decision 1 of #717 stands, and nothing is written to disk):

| Profile | Becomes |
|---|---|
| a point | a `from:` member at the point's address |
| a writable point `X` | also `Cmd.X`, a `to:` with `readback: X` |
| `derived: {from, bit}` / `{from, eq}` | an `expr:` (`StatusWord.3`, `Loop1Error = 39`) |
| `alarms:` | wiring alarms (§5) |
| the comms-loss alarm and `offline-alarm:` | the source's comms-loss alarm (§5) |
| `params:` and `{param}` | wiring params |
| a row's `endpoint`, `unit-id`, `params`, `driver:` | the instance's params and its source |
| `points:` selection | still selects the generated type's members (below) |

- **One adapter.** The wiring adapter replaces `devbind`'s struct adapter.
  The input side, quality roll-up, derived points and command routing exist
  once. `naut test`'s in-process face uses a fresh copy of it, as now.
- **Selection.** A row's `points:` still decides the generated type's
  members. Rows of one profile must still select the same points, as they
  do today. A plain type's members are fixed, so this rule is the profile's
  own and moves with it.
- **`naut devices render`** prints the wiring and the instance a row means,
  as YAML a user could paste into a wiring file. `update` compares the
  produced wiring (it already compares what a profile produces).
- **Faceplates** bind `Cmd.X` members instead of command tags. The asset
  page's setpoints and commands are the `Cmd` members, with their
  read-backs beside them (§10).
- **Unchanged:** virtual devices (they simulate the device, not the
  wiring), the plant model's `device:` key, I/O checkout evidence, the
  released-hash list (the profile format does not change, so no profile's
  `version:` moves), and the Logix and Siemens refusal.
- **IO-Link port devices** (`ports:`) produce wiring the same way in a later
  slice. Until then they keep today's path.

### 12.1 Migration: no silent break

Today a project writes `PUMP_ControlWord`. After this it writes
`PUMP.Cmd.ControlWord`. `alias:` cannot bridge the two: the nautilus runtime
ignores it, since it exists only for vendor targets. The migration is
therefore an explicit switch:

1. **This release:** `devices:` rows keep today's shape by default, with
   command tags and no `Cmd` member, so nothing changes under a project.
   `naut check` warns once per project: "devices: rows still make command
   tags (PUMP_ControlWord); run `naut devices migrate` to use
   PUMP.Cmd.ControlWord". A project opts in with one manifest key,
   `device-commands: members`.
2. **`naut devices migrate`** sets that key and rewrites every use it can
   find:
   - ST, LD and FBD sources;
   - test manifests (`given:`/`expect:`);
   - HMI and mimic `bind`s;
   - alarm and plant references.

   It prints what it changed and refuses to run on a dirty git tree.
   `examples/device-library` migrates in the same slice.
3. **The next minor release:** the default flips. A project without the key
   gets members, and `device-commands: tags` keeps the old shape with a
   deprecation warning.
4. **The minor after that:** `tags` is removed.

Sparkplug hosts see the type change as a new definition version (§11.1).
The host-side member scalar tags (`W6_Pump1_Speed`) follow the same three
steps, with `naut sparkplug import` regenerating them. (Q14)

## 13. The include file for rows

`wiring-files:` lists YAML files, each holding any of `wirings:`,
`instances:` and `devices:`:

```yaml
wiring-files: [wiring/pumps.yaml, wiring/tanks.yaml]
```

- Paths resolve inside the project, as `tag-files:` does. What `naut build`
  ships is what a reviewer sees.
- Decoding is strict: a misspelled key is an error with its file and line.
- Wirings, instances, devices and tags share one namespace. A duplicate is
  an error naming both files and lines.
- This is also the include file `devices:` rows never had, and the "its own
  file" half of decision 3.
- It has a JSON schema, served beside the manifest's, so the editor
  completes it.

## 14. Logix and Siemens writers

- **The `TYPE` is written as today**, with `EXTENDS` flattened (§8). An
  instance's type therefore reaches the controller, and so does any FB
  that takes it.
- **Wiring is refused at first.** `naut check` against a `logix` or
  `siemens` target refuses an instance with any `from:`, `to:`, `both:` or
  `expr:` member, and names it as our gap: "instance P101 is wired (source
  drive, Modbus); the Logix writer does not carry wiring yet (#NNNN), so
  nothing on the controller would fill it". It refuses `devices:` rows as
  now. A tracking issue is filed with slice 13.
- **A memory-only instance** (every member memory) is just a tag with
  initial values. It can be written today as a controller tag of the type,
  with its `set:` as the initial value (Q15).
- **Closing the gap later:** vendor I/O maps to alias tags, and an
  `expr:` maps to a routine that runs first. That is design work for a
  later brief, and is listed so nobody assumes it.

## 15. LSP and `naut check`: every refusal names its fix

| Refusal | The message says |
|---|---|
| a wiring member the type does not have | the member, the type, did-you-mean |
| two wirings giving one member different kinds | both wirings, the member, both kinds; "a member has one kind per type" |
| `both:` on a field-device driver | the driver; "use to: with readback:" |
| an instance missing a required param | the instance, the param, the wiring that declares it |
| a substituted address its driver cannot parse | instance, member, the value after substitution |
| `set:` on a non-memory member | the member and its kind |
| `wire:` that changes a kind | the member, both kinds; "a kind is the type's" |
| ST assigning a `from:` or `expr:` member (directly, or in a block it passes the instance to) | the line, the member, its kind; for a block, the block's line too |
| a whole assignment to an instance with `from:`/`expr:` members | the line; "assign the writable members" |
| an `expr:` that does not compile, or has the wrong type | the member, the ST error, the types |
| an `expr:` calling a function with a side effect | the function, its line, the rule (§6); "make it a program or a block" |
| an `expr:` cycle | every member in the cycle |
| `readback:` naming a member that is not `from:`/`both:`/`expr:` | the member and its kind |
| a generator needing `Cmd` where the user has a member of that name | the member; "rename it, or the generator cannot add commands" |
| `STRUCT EXTENDS` a non-struct, a repeated member, a cycle | the base, the member, both declarations |
| retained memory with no `retain:` section (a warning) | the instances; "add `retain: {}`" |
| a wired instance on a Logix or Siemens target | §14's wording, with the tracking issue |
| `devices:` rows on the old command shape (a warning, §12.1) | `naut devices migrate` |

The LSP shows the same diagnostics on the wiring file's line and on the ST
line. On an instance member it shows the kind, the source and the
read-back in hover, and completes member paths, params and sources.

## 16. The context-map row

The proposed row and rules go into `docs/design/context-map.md` and
`internal/contextmap` with slice 2, not in this docs-only PR:

| Context | Packages | Language | Upstream of | Translation |
|---|---|---|---|---|
| **Struct wiring** | `wiring` `wirebind` | wiring, instance, member kind (from, to, memory, expr, both), source, fill, param, read-back | Manifest, Device library (`devbind` produces wiring), Languages (the expression compile), CLI | **Published language** (`wiring`, a leaf: the file format, kinds, params, validation; imports only `lang/ir` and `lang/st`). `wirebind` derives each driver's manifest and the adapter that delivers an instance as one struct, so the drivers never learn of it |

**Rules:**

- `wiring` imports only `lang/ir` and `lang/st`.
- `wirebind` keeps `devbind`'s distance: no `runtime`, `server`, `alarm`,
  `acceptance`, `internal/project`, `internal/lsp`, `virtdev` or the CLI.
  It may import the drivers' manifest packages and `hw` (for `WriteDecl`).
- Field and IT drivers never import `wiring` or `wirebind`.
- `devbind` may import both.

**The Runtime** gets member kinds, partial delivery and the expression
stage as its own types (§2.3). It imports neither package.

**Glossary rows** (§4 of the map), since these words already mean other
things:

- **Instance**: IEC, a function block instance. Virtual devices: a
  simulated device (`virtdev.Instance`). Struct wiring: a tag of a wired
  type.
- **Parameter / param**: the parameters workflow: a retained scalar setpoint
  or memory member with a literal initial value. Struct wiring and device
  profiles: a `{name}` substituted into addresses (`params:`).
- **Source**: add "struct wiring: a named connection in a wiring".

## 17. Not adopted

- **Wiring inside the `TYPE`** (pragmas, attributes): the type must stay
  plain IEC for the writers and for users who copy it.
- **A new `role:` value** in `tags:`: an instance has no role, and its
  members have kinds.
- **Arithmetic in substitutions** (`address: "{base}+4"`): a per-instance
  `wire:` override covers the odd device, and a template language is a
  second compiler.
- **Selecting or omitting members per instance:** a type's members are
  fixed, so an FB can rely on them. The profile's `points:` is the
  profile's own rule (§12).
- **Changing a member's kind per instance:** it would make an FB's writes
  uncheckable (§2.4).
- **Atomic commands across sources** (§3).
- **Timers, edges or state in an `expr:`**: use a program or a block.
- **Retaining `to:` members:** a restart would replay a command.
- **Replacing `hw.WriteDecl`:** the wiring produces it.
- **Inheritance on the Sparkplug wire:** definitions are flat.
- **Generated wiring files from profiles:** profiles stay derive-at-load.
  `render` prints a wiring for a user who wants to leave the profile.
- **OPC UA structure fill:** later, at low priority, as James said.
- **Multiple bases for `EXTENDS`.**
- **A wiring registry:** sharing is a folder under git, as profiles are
  (#193).

## 18. PR plan

Each slice is small and has its own tests, each mutation-checked (break the
code, see the test fail, restore it). Each updates the guides it touches.

1. **This brief** (docs only).
2. **`wiring` format.** Decode (strict), validate (kinds, params, unknown
   members, one kind per member per type, `both:` refusal), the JSON schema
   and `wiring-files:`, plus the context-map row and its `internal/contextmap`
   rules. There is no behaviour yet. Tests: decode and refusal tables.
3. **Runtime members.** Member kinds in `TagDef`, partial delivery through
   the write mask, member writability in every write path (API, forces,
   OPC UA server, Sparkplug commands, `given:`), per-member quality with the
   root roll-up, and the `VAR_IN_OUT` mask rule. Tests:
   - a delivery leaves memory alone;
   - a POST to a `from:` member is a `400`;
   - copy-back never writes a `from:` member;
   - quality rolls up.
4. **Modbus and memory, end to end.** `wirings:` and `instances:` in the
   manifest, `wirebind` for Modbus, the call-site write check, retained and
   initial memory, params by member path, the LSP, and a small example.
   Tests include the redundancy replication test (§7).
5. **Expressions.** Compile, the purity check, the stage and its order,
   cycles, quality, and faults. Tests for each rule in §6, plus a
   no-allocation benchmark guard.
6. **OPC UA client, mixed sources, alarms.** Companions per source,
   `__Offline` per source, wiring alarms and instance alarm tuning, and
   `readback:` in `/api/meta`.
7. **Device profiles produce wiring.** One adapter, `render` prints wiring,
   `device-commands:`, `naut devices migrate`, faceplates on `Cmd`, the
   example migrated. Tests: a profile project's struct, alarms and writes
   are the same before and after the switch, apart from the command names.
8. **`STRUCT EXTENDS`** in Languages: the parser, the checker, flattening
   in `lang/ir`, the LSP, `stgen`, conformance cases, and the writers
   flattening it.
9. **EtherNet/IP fill.** `fill:`, extend and wrap, `both:` with the
   in-flight hold.
10. **Sparkplug.**
    - The edge: version, parameters (codec), member properties, the
      command refusal by kind, device placement.
    - The host: fill, `both:`, parameters.
    - The round-trip acceptance test.
11. **IT drivers.** `Cmd` members through `WriteDecl`, and the generated
    `hw` extensions.
12. **IO-Link port devices** produce wiring.
13. **Logix and Siemens:** the refusal wording, the tracking issue, and
    memory-only instances if Q15 says yes.

Slices 2 to 4 are the spine, and the rest can go in parallel after slice 4
(8 needs nothing but slice 2). Slice 7 is the reason for the order: the
longer `devices:` command tags are in released builds, the more projects a
migration touches.

## 19. Open questions

Each has a recommendation. A question that is James's call is marked.

1. **Instances in their own list, or as `tags:` rows with a `wiring:`
   key?** Recommend their own `instances:` list. A tags row would grow
   params, set, wire and alarm keys, and the tag-file row is a shared kernel
   every importer renders. Changing it costs every context.
2. **A member the wiring does not name is retained memory, with no word
   said?** Recommend yes. The unknown-member error catches typos, `render`
   lists every member's kind, and it is what makes user members on an
   imported type need no wiring at all.
3. **Retained memory without a `retain:` section:** warn (recommended) or
   add a `retain:` section implicitly? An implicit section would also start
   keeping every setpoint, which changes a project that never asked.
4. **A redeploy that changes `set:` or a type initial value:** recommend
   that the retained value wins and the change shows as repo-ahead in
   `naut params` (§7), matching setpoints today. The alternative, a deploy
   that resets changed values, would let a redeploy overwrite an operator's
   tuning silently. *James's call.*
5. **A retained member whose type changed:** discard it, start from the
   initial value, and log it (recommended). Converting the value, for
   example INT to REAL, is possible but would surprise more than it helps.
6. **Per-member quality on the wire:** recommend yes. The root shows the
   worst, with path entries for members that are not good. The HMI kit
   already reads them. Today one bad register greys a device's every member
   on screen, and after this only the member is grey while the root still
   shows the device as degraded.
7. **`VAR_IN_OUT` copy-back over an operator's mid-scan write to a memory
   member:** keep today's documented rule in v1 (recommended), or make the
   mask exact, so that copy-back commits only what the block assigned? The
   exact form is a runtime change to every in-out call, so do it only if
   someone hits it.
8. **Companion names with several sources:** `<Instance>_<source>__Online`
   per source, plus `<Instance>__Online` as their AND (recommended).
9. **Sparkplug placement of a mixed instance:** node level (recommended),
   while one with a single connection rides that device. The alternative,
   splitting one instance across devices, breaks "one type is one
   Template".
10. **Template version:** a content hash by default, with an explicit
    `version:` in the wiring for imported types (recommended), or always
    explicit? Explicit-only relies on people remembering to bump it, which
    the profile library already showed needs a test to enforce.
11. **Round-trip fidelity:** member properties carrying the IEC type, the
    enumeration, the bounds and the kind (recommended), or change the wire
    datatypes to honest widths (Int16 for INT)? Changing the datatypes
    breaks every existing host's view of the edge.
12. **Peer importers' writable members:** `both:` in place (recommended,
    §9), or a generated `Cmd` sub-struct as the 11-28 comment's wording
    says? `Cmd` there would make the imported type differ from the peer's,
    and the round trip in §11.3 would fail. *James's call*, since it reads
    against his comment.
13. **The `both:` in-flight hold:** hold until the write is answered or the
    peer's next data carries the member, bounded by a few polls
    (recommended), or no hold and accept the flicker?
14. **Migration window:** opt-in now, default flips next minor, removed the
    minor after (recommended, §12.1), or one release with both shapes at
    once? Both shapes at once means two tags bound to one register, and two
    writers.
15. **Memory-only instances on Logix and Siemens:** write them as tags of
    the type now (recommended, it is cheap and unblocks FBs taking them),
    or refuse every instance until the wiring gap closes?
16. **A `not-reached:` alarm shorthand** for `readback:`: later
    (recommended). An `expr:` plus an alarm covers it in v1.
17. **Params inside `expr:`** by text substitution: yes (recommended). It
    is the same rule as in addresses.
18. **`expr:` reading project tags**, not only siblings: yes, read-only
    (recommended). A sibling shadows a tag, with a warning.
19. **Where #1458 sits in the order** (bugs, claims, coverage, fuzz).
    Recommend slices 2 to 4 right after the in-flight bug work, because the
    device-library migration grows with every release. *James's call.*
20. **`STRUCT EXTENDS` as a nautilus extension** beyond IEC: accept it
    (recommended). The CODESYS spelling reads naturally, flattening keeps
    every writer and wire format plain, and wrap remains the pure-IEC path.
    *James's call* under "language stays vendor-neutral".
21. **The keyword `fill:`** for a driver filling a type whole: keep it, or
    `from:` at the wiring level? Recommend `fill:`, so a reader never
    confuses "the whole type" with "this member".
