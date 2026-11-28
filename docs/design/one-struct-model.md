# Design: one struct model (types and wiring)

**Context:** a new context, *Struct wiring* (§16), between the Manifest and
the field and IT drivers. It changes the Runtime (members with their own
kinds, §2.3) and the Device library (profiles produce wiring, §12). Driver
names stop at the driver packages, as now. Core: the type a program sees is
the product. Issue #1458; related #717. Status: **approved by naut-lead**;
James said on 11-28 to proceed. Q4, Q12, Q20 and Q24 wait on James (§19),
and each gates one later slice.

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
reopens (the issue's words; it is true today only for the drivers whose gate
mode says so, and §2.3 says how a wired member keeps it). `hw.WriteDecl` is shared with the IT drivers. The `TYPE` stays plain
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
  device member into memory without a word. Forgetting an entry
  altogether would, so for a wiring with any source, `naut check` prints
  one info line listing its implicit memory members. An alarm or a
  `readback:` on an implicit memory member is an error: a forgotten `Fault`
  would otherwise alarm on an operator-writable value. (Q2)
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
| **First scan** | its initial value, else zero, quality `notConnected` until delivered (every wiring source is a communications driver) | its initial value, else zero, as a seed only; not handed to the driver until something writes it in this leadership (§2.3) | the retained value, else `set:`, else its initial value, else zero | `set:`, else its initial value, else zero | evaluated on the first scan, `notConnected` until its inputs arrive | as `from:` |
| **Kept across a restart** | never | never: a restart must not replay a command | yes (with a `retain:` section, Q3) | never | never | never |
| **On a standby** | not read (stale) | not written | not changed there; writes go to the leader | not changed there | not evaluated (stale), since a standby runs no logic | not read, not written |
| **After a failover** | read on the new leader's first scan | not sent until written in the new leadership (§2.3); then the driver's own rules | reloaded from the retain store or the streamed image | as a `state` tag without `retain: true`: carried in the image on a hot takeover, the new leader's own copy on a cold one | evaluated on the new leader's first scan | delivered first, then sent only once written again (§2.3) |

### 2.3 What the runtime does

Today the store holds whole tags, the driver replaces a whole input before
each scan, and the role belongs to the root tag (`runtime/runtime.go`
`setMany`). The instance needs four changes, all in the Runtime context. The
manifest hands them in as runtime types, so the Runtime never imports the
wiring.

1. **Member kinds.** `runtime.TagDef` gets the instance's `InstanceDef`
   (below), with the kind of each member path (additive, since `runtime` is
   stable Go API). The external write paths (`POST /api/tags`, the OPC UA
   server, Sparkplug commands) check the path against it. Forces,
   substitutes and `given:` are not external writes (below). A whole-tag write to an
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
The Sparkplug host takes it as a baseline and sends nothing. The driver
conformance suite gets a wired-instance row per driver, so the adapter
cannot quietly change a mode. There are three gate modes
(`internal/driverconformance`):

- **`dropAndAsk`** (Modbus, EtherNet/IP, OptoMMP, PLCnext): when the gate
  reopens, and on a takeover, the driver asks for every output and sends
  each one again with the program's current value. A `to:` member on these
  drivers is therefore a **level**: it is re-asserted.
- **`holdAndDeliver`** (the Sparkplug host): the driver keeps the latest
  command and sends it when the gate reopens.
- **`dropAndRebaseline`** (SNMP, Redfish, Prometheus): the first push after
  the gate reopens is a baseline, so a command is never sent twice.
- The **OPC UA client** is not in the suite's list, but it behaves like
  `dropAndAsk`. It re-sends every output when the gate reopens, and on a
  re-established session unless `reassert-on-reconnect: false` is set
  (opcua-client guide).

So "no command is replayed" holds only where the driver's mode says so.
Under `dropAndAsk` a write-to-trigger command goes out again on every
regain, as the EtherNet/IP guide warns today. For that case a `to:` member
takes **`event: true`**. The first value of an event leaf after a gate
reopen, a takeover or a reconnect is a **baseline** and is not sent. Only a
later change of value goes out. Assigning the same value again does not
count here, although the tag model counts it as a write elsewhere:
`PumpCtl` (§2.4) assigns `Cmd.Start` on every call, and a held TRUE must
not go out again after a reopen. That is the `dropAndRebaseline` rule,
applied per member on any driver, and it is a slice 4 test. A profile
point may declare `event: true` too, and its generated `Cmd` member
inherits it. (Q22)

**A `to:` leaf is written per leadership.** After a restart or a takeover, a
`to:` leaf that nothing has written in this leadership is not handed to the
driver. That holds whatever the leaf's value is and whether or not a
streamed image carried it over. The program usually writes its commands on
its first scan, so they go out as they do today. An operator's
`Cmd.SpeedRef` from before the takeover is not re-sent from a copy that may
be stale. The device still holds what it was last given. Today the same
rule exists per tag ("except a typed one with no `init:` that nothing has
written yet"), and this makes it per leaf. `to:` leaves are never saved to
the retain store, and a test proves one never comes back from a retain
file.

**A `to:` leaf's initial value is a seed, never a command.** This is
unlike an output tag with an `init:`, which goes to the driver on the first
scan. A `TYPE` initial value on a `to:` member only sets what the program
reads before it writes the member.

**Adopting the read-back.** A `to:` leaf that is unwritten in this
leadership and has a `readback:` of the same type takes the read-back's
value when it is first delivered. This is the Sparkplug host's "a member
output adopts the site's live value" rule, generalized. The adopted value
is not a command, but it means "commanded, not reached" (§10) does not alarm
on every restart. An `event:` leaf never adopts: a `Cmd.Start` with
`readback: Running` would otherwise adopt TRUE. A program that reads an
adopted leaf and assigns it back (a seal-in) has written it, so from then
on it is a command, as any assignment is. (Q23)

**`both:` leaves are never pushed before they are delivered** in this
leadership. The first delivery is the baseline, and a full push after a gate
reopen or a takeover sends a `both:` leaf only if it was written in this
leadership after that delivery. A stale copy therefore never overwrites what
another client of the peer wrote meanwhile. **A write before the first
delivery:** an external write (API, OPC UA, Sparkplug) is refused, naming
the cause ("P101.SpeedSP has not been read from the peer yet"). A program's
assignment is not pushed, and the delivery replaces it. This rule wins over
the Sparkplug host's `holdAndDeliver`, which today sends a member command to
a site never seen born. A `both:` member is never commanded blind. Today's
member scalar tags keep the host's rule until the §12.1 switch. (Q25)

**Forces, substitutes and test inputs.** A force, a substitute and a
test's `given:` may set `from:` and `both:` members. That is what those
features are for, and they are not the API writes the kinds refuse. A force
may also hold an `expr:` member, which the stage then skips while it is
forced, so an alarm can be tried. The order in a scan is: deliver,
substitute, force, evaluate `expr:`, run the programs.

**What the consumers read.** The manifest hands the runtime one
`runtime.InstanceDef` per instance. It holds the wiring's name, the
Template version, the params with this instance's values, and for each
member path its kind, `readback:`, `event:`, unit and desc. The Sparkplug
edge, the server (`/api/meta`) and the OPC UA server read it through the
runtime they already import, and none of them imports `wiring`.

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

```iecst
FUNCTION_BLOCK PumpCtl
  VAR_IN_OUT P : Pump; END_VAR
  VAR_INPUT Demand : BOOL; END_VAR
  P.Cmd.Start := Demand AND P.Healthy AND NOT P.Running;
  P.Cmd.Stop  := NOT Demand AND P.Running;
  IF P.Running THEN
    P.RunHours := P.RunHours + 0.1 / 3600.0;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM Main
  VAR Ctl1, Ctl2 : PumpCtl; END_VAR
  Ctl1(P := P101, Demand := Level > StartLevel);
  Ctl2(P := P102, Demand := Level > StartLevel + 10.0);
END_PROGRAM
```

- **`VAR_INPUT`** copies the instance in. Nothing is written back.
- **`VAR_IN_OUT`**: today the tag model counts every member as assigned on
  every call, because the call copies the pin back. For an instance that
  rule would be unsafe. Copy-back would count as the first write of every
  `to:` leaf, so on scan one Modbus or EtherNet/IP would decide a zero
  `Cmd.*` against the device and write it. It would also lose an
  operator's mid-scan write to a `to:`, `both:` or `memory:` leaf. So for a
  wired instance **the mask is exact**: copy-back commits only the leaves
  the block actually assigned on that call, **recorded at run time through
  the pin**. A static set from the checker would bring the hazard back for
  a conditional assignment (`IF x THEN P.Cmd.Start := TRUE; END_IF` would
  copy back a zero as a first write on every call where `x` is false).
  Slice 3's tests cover a conditional assignment, a pin passed on to an
  inner block, a sub-struct pin (`P101.Cmd`) and an array element. In the
  example, `PumpCtl` commits `Cmd.Start`,
  `Cmd.Stop` and, while running, `RunHours`, and nothing else. The
  existing rule stays for plain structs (Q7). `from:` and `expr:` leaves
  are never in the mask, since the check (below) refuses a real assignment.
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

- **Each connection has its own write gate, health and `__Online`.** A
  shared source is one connection for every instance that uses it. There is no atomic command across sources:
  two `to:` members on two sources changed in one scan are two independent
  writes, in no guaranteed order. A sequence that needs order writes one,
  waits for its read-back, then writes the next.
- **Companion tags are per connection, not per instance.**
  - An instance that reads exactly one connection, its own (its own
    `endpoint`), gets `<Instance>__Online` (and `__Outages` for Modbus), as
    a `devices:` row does today.
  - An instance that reads more than one connection of any kind names each
    connection of its own `<Instance>_<source>__Online`.
  - A source that shares one connection among all instances (no `endpoint`,
    as `level:` above) uses that connection's own companion
    (`plant_ua__Online`), so one server outage is one companion going false,
    not one per tank.
  - In the multi-connection case `<Instance>__Online` is only the AND of
    them all, and it raises no comms-loss alarm of its own.

  In the Tank example, then, the companions are `T1_pump__Online`, the
  shared `plant_ua__Online`, and `T1__Online` as their AND. (Q8)
- **Alarms are suppressed per connection.** A member's alarm carries the
  `enable:` of its own connection's `__Online`. Each connection raises one
  comms-loss alarm (§5), so a shared server that goes down raises one alarm,
  not N.
- **Sparkplug.** Every wired instance publishes at **node level**. Riding a
  connection's Sparkplug device would make the instance's memory and `Cmd`
  members die at DDEATH, reachable only by DCMD, and adding a second source
  would move the metric. The connection's health still reaches the host:
  its declared `__Online` companion rides its device as now. **The cost:**
  today a dead connection's device dies, so a host never shows stale values
  as fresh (sparkplug guide), and the edge publishes no per-metric quality.
  At node level the instance stays born. So while a connection is down, the
  edge publishes that instance's `from:`, `both:` and `expr:` members that
  depend on it as stale, using the property §11.1 names (`Quality`), and a
  value never delivered is sent with `is_null`, until the connection answers
  again. This moves
  today's `devices:` struct tags from their device to node level, and is
  part of the §12.1 switch and its report. (Q9)

## 4. Per-instance params and overrides

**Params.** A wiring declares `params:` with defaults. `null` means each
instance must give it. A value is substituted as `{name}` into source keys
and member address keys. `devprofile`'s `{name}` syntax is reused
(`Profile.Substitute`), but today it fills only OPC UA node ids and IO-Link
ids, and a row's `endpoint` is a key of its own. Substituting into source
keys (`endpoint`, `unit-id`) and numeric address keys (`address`) is new.
The text is substituted first and then parsed as the key's own type
(`address: "{reg}"` with `reg: 40` is the integer 40). Two params come
built in: `{name}` (the instance's
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
| `offline-alarm` | per connection the instance owns (not a shared one), as a `devices:` row's (§5) |
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

- each rule carries `enable:` set to its member's connection's `__Online`.
  An `expr:` member's rule carries the AND of the connections it reads.
  A `memory:` member's rule has none;
- each **connection** raises one **comms-loss alarm**. A per-instance
  connection's is `<Instance>__Offline` when it is the instance's only
  connection, else `<Instance>_<source>__Offline` (the companion names of
  §3), tuned or turned off by the instance's `offline-alarm:`.
  A shared connection's is named after its driver (`plant_ua__Offline`) and
  tuned in the wiring's `sources:` entry. It fails active and has the same
  defaults (high, class `comms`, 5 s on-delay).

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
of an instance are saved. Its `from:`, `to:`, `both:`, `expr:` and initial
members are not. The retain store already brings a struct back member by
member onto the current tag, so loading needs no new format. Saving does
change: today the store saves whole struct tags (`runtime/retain.go`), so
it gains a filter by member kind. A test proves that a `to:` value written
into a retain file by hand is still not applied.

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

**`memory: initial`** behaves exactly as a `state` tag without
`retain: true` does today. It starts from its initial value on every
restart. On a hot takeover the streamed image carries it (the image holds
every tag the field does not provide). On a cold takeover the new leader
keeps its own copy, which is its initial value if it has never led.

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

**`STRUCT EXTENDS` is a language change of its own** (slice 11). Nothing in
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
After that, delivery is the truth again. (Q13) On a gate reopen or a
takeover a `both:` leaf follows §2.3: it is not pushed until it has been
delivered in this leadership, and it is pushed after that only if it was
written again. A full push never sends the peer a copy it may have changed
since.

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
  that has an on-delay. After a restart, `Cmd.SpeedRef` adopts `Speed`'s
  value at the first delivery (§2.3, Q23), so the alarm does not fire
  merely because the controller restarted while the drive kept running.
  Where the types differ and nothing is adopted, the built-in function
  `WRITTEN(Cmd.SpeedRef)` says whether the leaf has been written in this
  leadership, and the expression guards on it. It only reads, so §6 allows
  it. As built (slice 8c), it belongs to `expr:` members only and takes a
  `to:` leaf; it is no name a program loses (a project FUNCTION, block or tag
  `WRITTEN` is refused only by an expression that calls it). A program's
  `WRITTEN` would reserve the name everywhere, so it waits for a need. A shorthand that
  generates the pair is Q16.

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
- **One definition per type, whatever its wirings.** A type may have
  several wirings (§1), but a host sees one Template per type. So the
  version and parameters belong to the type: they are computed over all of
  its wirings.
- **Version.** The definition's `version` is a short hash of what defines
  it: the member names, types, order and kinds (one per type, §2.4) and the
  published param names. A change a host must notice changes it, and nobody
  has to remember to bump it. A wiring may set `version:` explicitly, which
  an imported type does in order to keep the version its source published.
  Two wirings of one type that set different versions are a check error.
  (Q10)
- **Parameters.** The published params of all the type's wirings (their
  union) become the definition's Template parameters. A default is given
  where every wiring that declares the param agrees on it. Each instance
  carries its own values, the way SCADA UDT parameters map onto them. A
  param marked `publish: false` is left out, and a param that feeds a
  source's `endpoint` is left out unless it is marked `publish: true`, since
  addresses do not belong on a broker by default. `set:` and `wire:` are
  not published: `set:` values are already visible as the memory members'
  values, and `wire:` is addressing. The 11-28 comment says "instance
  params (`prefix`, overrides)", so leaving overrides out is Q24. This
  needs the parameters field in the payload codec (`sparkplug/spb` and
  `payload.go`).
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
  input is now. A command writes the store like any other write, so what
  reaches the device follows §2.3: the driver's gate mode, `event:`, and
  the per-leadership rule. A Sparkplug command is never replayed by the
  edge itself.
- **Members Sparkplug cannot carry.** Sparkplug has no array of Templates
  and no multi-dimensional array. The edge leaves such members out of the
  definition and every instance today (`sparkplug/templates.go`), and it
  keeps doing so for wired types: they are logged once, and `naut check`
  warns on a wired type with such a member when the project has a
  `sparkplug:` section. The round-trip test (§11.3) covers only the members
  Sparkplug can carry.
- **Node level.** Every wired instance publishes at node level (§3), so its
  commands are NCMDs. While one of its connections is down, the members
  that depend on it carry a stale quality (or `is_null` when never
  delivered), so a host is not shown stale values as fresh. That replaces
  the device death a host gets today. The convention:
  - **The property** is `Quality`, an Int32, in the metric's property set.
    A metric without it is Good, so a healthy instance pays nothing. This
    is Cirrus Link's convention for its Ignition MQTT modules ("If the
    payload is missing the quality property, it should be considered to be
    Good", [Cirrus Link, message payloads](https://docs.chariot.io/x/8YFpCw)).
    It is not part of the Sparkplug 3.0 specification.
  - **The values** are Ignition quality codes: Good is 192, Bad_Stale is
    516 and Bad_NotConnected is 522
    ([Ignition 8.3, quality codes](https://docs.inductiveautomation.com/docs/8.3/platform/tags/quality-codes-and-overlays)).
    A member of a connection that is down is sent as Bad_Stale. One never
    delivered is sent as Bad_NotConnected, with `is_null`.
  - **Not yet verified:** how a Bad code is encoded in the Int32. Cirrus
    Link's example payloads carry `-2147483133`, which is 0x80000000 plus
    515 (Bad_Disabled). A forum account
    ([Inductive Automation forum](https://forum.inductiveautomation.com/t/tag-quality-via-sparkplug/34456))
    says the top bit marks Bad. The Ignition page gives no bit layout, and
    nothing official says that MQTT Engine reads the property as a host
    (the docs show MQTT Transmission writing it). **Slice 13 verifies both
    against a running Ignition with MQTT Engine before shipping**, and
    naut-lead hears the result before the slice starts.
  - **Hosts that would no longer see a device go stale:** any host that
    ignores metric properties. Today that includes nautilus's own Sparkplug
    host, which reads no property (slice 14 makes it read `Quality`). It
    also includes, as far as this brief can tell, every host other than
    Ignition: no public documentation was found for another host reading
    it. Geo SCADA has a Sparkplug quality mapping page that could not be
    read, so it is unverified. Such a host sees the node alive and the
    instance's last values with no change; its `__Online` companion still
    dies with its Sparkplug device. This line goes in the §12.1 migration
    report.
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
  released-hash list (the profile format gains only the additive `event:`
  key of Q22, so no released profile's hash or `version:` moves), and the Logix and Siemens refusal.
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
   - alarm and plant references;
   - `opcua.writable` and `sparkplug.writable` patterns;
   - historian tag lists and `auth:` area lists;
   - parameter set files in the project.

   It prints what it changed and refuses to run on a dirty git tree. The
   report also lists what it cannot rewrite because it lives outside the
   project: every Sparkplug metric that disappears (`PUMP_ControlWord`) or
   moves (a device's struct tag moves from its device to node level, §3),
   and every OPC UA node that changes. Those are the hosts and clients to
   update. It also says, once per project with a `sparkplug:` section:
   "staleness of a device's members now travels as the `Quality` metric
   property (Ignition's convention), not as a DDEATH; a host that ignores
   metric properties (any host other than Ignition, as far as we know, and
   nautilus's own host before slice 14) will no longer see these members go
   stale, only the device's `__Online` companion". `examples/device-library` migrates in the same slice.
3. **The next minor release:** the default flips. A project without the key
   gets members, and `device-commands: tags` keeps the old shape with a
   deprecation warning.
4. **The minor after that:** `tags` is removed.

A Sparkplug host sees more than a new definition version (§11.1): metrics
disappear and move. That is why the switch is opt-in and reported. The
host-side member scalar tags (`W6_Pump1_Speed`) follow the same three
steps, with `naut sparkplug import` regenerating them. (Q14)

## 13. The include file for rows

`wiring-files:` lists YAML files, each holding any of `wirings:`,
`instances:` and `devices:`:

```yaml
wiring-files: [wiring/pumps.yaml, wiring/tanks.yaml]
```

- **Slice 2 reads `wirings:` and `instances:` only.** A `devices:` key in a
  wiring file is refused as an unknown key until slice 10, which is where
  device rows get their include file.
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
  now. This refusal ships with the first instances (slice 5), and so does
  the tracking issue it cites.
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
| an alarm or `readback:` on a member the wiring does not name (implicit memory) | the member; "wire it (from:, expr:), or write memory: if it is meant to be memory" |
| two wirings of one type setting different `version:` | both wirings and both versions |
| `event:` on a member that is not `to:` | the member and its kind |
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
stage as its own types (§2.3). `runtime.InstanceDef` carries the kinds,
read-backs, params and version to the Sparkplug edge, the server and the
OPC UA server. The Runtime imports neither package.

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
"Needs" lists the slices that must be merged first.

1. **This brief** (docs only).
2. **`wiring` format.** Decode (strict), validate (kinds, params, unknown
   members, one kind per member per type, `both:` refusal, substitution
   typing), the JSON schema and `wiring-files:`, plus the context-map row
   and its `internal/contextmap` rules. There is no behaviour yet. Tests:
   decode and refusal tables. *Needs 1.*
3. **Runtime members.** `InstanceDef` in `TagDef`, partial delivery through
   the write mask, kind checks on the external write paths (API, OPC UA
   server, Sparkplug commands), forces, substitutes and `given:` on
   `from:` members, per-member quality with the root roll-up, and the exact
   `VAR_IN_OUT` mask for instances. Tests:
   - a delivery leaves memory alone;
   - a POST to a `from:` member is a `400`;
   - copy-back commits only what the block assigned (the four §2.4 cases);
   - quality rolls up. *Needs 2.*
4. **The push rules.** The per-leadership `to:` rule, `event:` as a
   baseline after a reopen, takeover or reconnect, the `both:` baseline and
   its refusal before the first delivery, and read-back adoption. Tests:
   - an unwritten `to:` leaf is not pushed at start or after a takeover;
   - an `event:` leaf assigned TRUE every scan is not re-sent after a gate
     reopen;
   - a `both:` leaf is not pushed before its delivery;
   - an `event:` leaf never adopts. *Needs 3.*
5. **Modbus instances, end to end.** `wirings:` and `instances:` in the
   manifest, `wirebind` for Modbus, and companions. The Logix/Siemens
   refusal ships here, from the first day instances exist, with the wording
   of §14, and the tracking issue it cites (#1478) is filed with this slice.
   **Hard condition** (naut-lead, 11-27), met before instances are enabled:
   no push key is silently dropped. `runtime.New` refuses an instance whose
   `to:` or `both:` leaf no driver takes as an output, or whose `from:` or
   `both:` member no driver delivers, naming the member and its key, when
   the driver declares its tags; `io.Multi` logs, once per key and listed by
   `Unclaimed()`, any write no child claims. Test: a command to an
   unclaimed key fails loudly. What slice 5 does not run yet (`expr:`,
   `fill:`, wiring and instance alarms, `offline-alarm:`, `set:`, a source
   on any driver but Modbus, an instance on more than one connection) is
   refused at load, naming its slice. Memory is not retained yet (slice 6):
   `naut check` warns, naming the members. A wiring edit applies at a
   restart, never through the live I/O apply. *Needs 4.*
6. **Memory.** Retained and initial memory, the retain-store filter,
   `set:`, params by member path, and the redundancy replication test (§7).
   *Needs 5.*
7. **The call-site write check and the FB example.** The checker's
   refusals for ST, LD and FBD, the LSP diagnostics and hover, and §2.4's
   `PumpCtl` as an example with a sequence test. *Needs 5.*
8. **Expressions.** Compile, the purity check, the stage and its order,
   cycles, quality, faults, `WRITTEN()`, and forcing an `expr:`. Tests for
   each rule in §6, plus a no-allocation benchmark guard. *Needs 5.*
9. **OPC UA client, mixed sources, alarms.** Shared and per-instance
   connections, companions and `__Offline` per connection, wiring alarms
   and instance alarm tuning, and `readback:` in `/api/meta`. *Needs 5.*
10. **Device profiles produce wiring.** One adapter, `render` prints wiring,
    `event:` per point, `device-commands:`, `naut devices migrate` and its
    report, faceplates on `Cmd`, and the example migrated. Tests: a profile
    project's struct, alarms and writes are the same before and after the
    switch, apart from the command names and the Sparkplug placement.
    *Needs 6, 8, 9.*
11. **`STRUCT EXTENDS`** in Languages: the parser, the checker, flattening
    in `lang/ir`, the LSP, `stgen`, conformance cases, and the writers
    flattening it. *Needs 2 only.*
12. **EtherNet/IP fill.** `fill:`, wrap, extend, and `both:` with the
    in-flight hold. **Hard condition** (naut-lead, 11-27), met before any
    `both:` binding is enabled (here and in slice 14): the in-flight hold and
    peer dedupe. A `both:` leaf the program assigns every scan must not be
    re-sent every scan while the peer shows another value (a write storm to
    a peer PLC). Test: N scans of the same assigned value give one write.
    *Needs 5; extend needs 11.*
13. **Sparkplug edge.** Version, parameters (codec), member properties, the
    command refusal by kind, node-level placement and the stale-quality
    property for a connection that is down. *Needs 6, 8.*
14. **Sparkplug host and the round trip.** Fill, `both:`, parameters,
    reading the `Quality` property, and the round-trip acceptance test.
    The slice 12 hard condition holds here too: no `both:` binding before
    the in-flight hold and peer dedupe, with the same N-scans-one-write
    test. *Needs 11, 12 (`fill:`), 13.*
15. **IT drivers.** `Cmd` members through `WriteDecl`, and the generated
    `hw` extensions. *Needs 11.*
16. **IO-Link port devices** produce wiring. *Needs 10.*
17. **Logix and Siemens:** memory-only instances, if Q15 says yes.
    *Needs 6.*

Slices 2 to 5 are the spine. After 5, slices 6 to 9 and 11 can run in
parallel. Slice 10 is the reason for the order: the longer `devices:`
command tags are in released builds, the more projects a migration touches.

## 19. Open questions

Each has a recommendation. naut-lead accepted the recommendation for every
question not marked *James, pending* (Q9 with a condition, recorded there).
The four pending ones are in James's decisions doc, and each gates one
slice: Q4 gates slice 6, Q12 slice 12, Q20 slice 11 and Q24 slice 13.
Nothing in slices 2 to 5 waits on them.

1. **Instances in their own list, or as `tags:` rows with a `wiring:`
   key?** Recommend their own `instances:` list. A tags row would grow
   params, set, wire and alarm keys, and the tag-file row is a shared kernel
   every importer renders. Changing it costs every context.
2. **A member the wiring does not name is retained memory?** Recommend yes,
   with the safeguards in §1: an info line listing implicit memory members
   when the wiring has sources, and an error for an alarm or `readback:` on
   one. It is what makes user members on an imported type need no wiring at
   all. The alternative, requiring `memory:` on every member once a wiring
   has sources, is stricter and wordier.
3. **Retained memory without a `retain:` section:** warn (recommended) or
   add a `retain:` section implicitly? An implicit section would also start
   keeping every setpoint, which changes a project that never asked.
4. **A redeploy that changes `set:` or a type initial value:** recommend
   that the retained value wins and the change shows as repo-ahead in
   `naut params` (§7), matching setpoints today. The alternative, a deploy
   that resets changed values, would let a redeploy overwrite an operator's
   tuning silently. *James, pending* (gates slice 6).
5. **A retained member whose type changed:** discard it, start from the
   initial value, and log it (recommended). Converting the value, for
   example INT to REAL, is possible but would surprise more than it helps.
6. **Per-member quality on the wire:** recommend yes. The root shows the
   worst, with path entries for members that are not good. The HMI kit
   already reads them. Today one bad register greys a device's every member
   on screen, and after this only the member is grey while the root still
   shows the device as degraded.
7. **The exact `VAR_IN_OUT` mask for plain structs too?** It is required
   for instances (§2.4). Recommend leaving plain structs on today's
   documented rule until someone hits it, since changing them touches every
   in-out call in every project.
8. **Companion names:** one per connection, `<Instance>__Online` for a
   per-instance connection (with `_<source>` when there are several), the
   driver's own companion for a shared one, and `<Instance>__Online` as the
   AND of everything the instance reads (recommended, §3).
9. **Sparkplug placement:** every wired instance at node level
   (recommended, §3), or ride the device when there is one connection? Node
   level keeps memory and `Cmd` members alive across a DDEATH and keeps a
   metric from moving when a source is added. The costs: today's
   `devices:` struct tags move, which the §12.1 switch reports; and staleness
   reaches a host as the `Quality` metric property (Ignition's convention,
   §11.1) instead of a DDEATH, which a host that ignores properties does not
   see. **Accepted by naut-lead on that condition**: the property, the codes
   and the hosts that lose the signal are named in §11.1 and in the §12.1
   report, and the Int32 encoding is verified against Ignition before
   slice 13.
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
    and the round trip in §11.3 would fail. *James, pending* (gates slice
    12), since it reads against his comment.
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
19. **Where #1458 sits in the order:** answered. James said on 11-28 to
    proceed now, so slice 2 starts once this brief merges.
20. **`STRUCT EXTENDS` as a nautilus extension** beyond IEC: accept it
    (recommended). The CODESYS spelling reads naturally, flattening keeps
    every writer and wire format plain, and wrap remains the pure-IEC path.
    *James, pending* (gates slice 11), under "language stays
    vendor-neutral".
21. **The keyword `fill:`** for a driver filling a type whole: keep it, or
    `from:` at the wiring level? Recommend `fill:`, so a reader never
    confuses "the whole type" with "this member".
22. **`event: true` for write-to-trigger commands** on `dropAndAsk`
    drivers (§2.3): recommend it, defaulting to a level (re-asserted), which
    is what those drivers do today. The alternatives are making every `Cmd`
    member an event (which would lose re-assertion of a drive control word
    after a takeover) or only documenting the hazard. The profile format
    gains `event:` per point, so a reset or a pulse command says so once.
23. **A `to:` leaf adopting its read-back** after a restart or takeover
    (§2.3): yes, when the types match and the leaf is not an `event:`
    (recommended). It is the Sparkplug host's existing rule. Without it, a
    "commanded, not reached" alarm fires on every restart while the device
    keeps running. It comes with a new built-in, `WRITTEN(<path>)`, true
    once a `to:` leaf has been written in this leadership, for read-backs of
    another type (§10). Recommend adding it; the alternative is a
    `<path>__Written` companion per leaf, which multiplies tags.
24. **Template parameters for overrides:** the 11-28 comment says
    "instance params (`prefix`, overrides)". Recommend publishing params
    only. `set:` values already appear as members, `wire:` is addressing,
    and params feeding an `endpoint` are left out unless marked
    `publish: true` (§11.1). *James, pending* (gates slice 13), since it
    narrows his comment.
25. **A `both:` write before its first delivery** (§2.3): refuse an
    external write, naming the cause, and let the delivery replace a
    program's assignment (recommended). This overrides the Sparkplug host's
    `holdAndDeliver` for `both:` members, where today a member command to a
    site never seen born is sent on its birth. Commanding a peer value
    nobody has read is the hazard. Today's member scalar tags keep the old
    rule until the §12.1 switch.
