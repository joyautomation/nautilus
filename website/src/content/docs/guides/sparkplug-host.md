---
title: Consuming a Sparkplug fleet
description: A manifest project as a Sparkplug B host application — consume a whole group of edge nodes as INPUT tags, write back via NCMD/DCMD, and pass the TCK host-application profile.
---

A `driver: {type: sparkplug-host}` section makes a project the **other
side of the wire** from [the edge-node guide](/guides/sparkplug/): instead
of publishing one controller's tags, it subscribes to a whole Sparkplug B
group and presents every edge node's data as `role: input` tags. Operator
writes go back out as NCMD/DCMD. Shape: a manifest-tier `io.Driver`, plus
`naut sparkplug import` to generate what it needs — mirroring
`naut eip import` end to end.

```yaml
driver:
  type: sparkplug-host
  broker: "tcp://mqtt.plant:1883"    # ssl://host:8883 for TLS
  group-id: Plant
  host-id: plant-scada               # STATE topic spBv1.0/STATE/plant-scada
  manifest: sparkplug_manifest.yaml
  primary: true                      # publish STATE (default)
  state-form: both                   # 3.0 JSON + legacy STATE/<host-id>
  reorder-timeout: 5s                # unfilled seq gap this long -> NCMD Rebirth
  stale-after: 2m                    # silence this long -> node marked stale
  on-unknown: log                    # metrics the manifest never bound: log | ignore | strict
tag-files: [tags/sparkplug.yaml]
```

Password (if the broker needs one) is **never in the file**:
`NAUTILUS_MQTT_PASSWORD`, the same rule the edge-node `sparkplug:` section
keeps. `broker`, `host-id`, `group-id` (or `group-ids`), and `manifest`
are required — a project missing one fails `naut check`, not
`naut run`, so a bad config never reaches the field.

## Generating the manifest: `naut sparkplug import`

Three files are generated and committed — never hand-edited:
`sparkplug_types.st` (Sparkplug Templates as ST `TYPE`s, composed
automatically as a library — no entry needed in `nautilus.yaml`),
`sparkplug_manifest.yaml` (node/device/metric bindings, validated against
every birth at runtime), and `tags/sparkplug.yaml` (the tag list, via the
same `internal/tagfile.Render` `naut eip import` uses).

Two ways to generate them, producing byte-identical output for metrics
they share:

```sh
# Live: listen to a broker, ask every node heard from to rebirth
# (births aren't retained), generate from what birthed.
naut sparkplug import --broker tcp://mqtt.plant:1883 --group Plant

# Offline: from a committed site list — no broker, so CI can build a
# ~60-site central project with none of them online, and review a diff
# before the field work happens.
naut sparkplug import --sites sites.yaml --out .
```

```yaml
# sites.yaml
group: Plant
types:
  - name: Motor
    fields: [{ name: Speed, type: Double }, { name: Run, type: Boolean }]
sites:
  - node: W6                          # the Sparkplug edge_node_id
    metrics:
      - { name: Well/Level, type: Double }
      - { name: Pump1,      type: Motor, writable: [Speed] }  # a Template instance
    devices:
      - device: plc1
        metrics:
          - { name: Pump/SpeedSP, type: Double, writable: true, init: 0.0 }
```

`naut sparkplug browse --broker ... --group ...` prints what's on the
wire without generating anything; `naut sparkplug tags
sparkplug_manifest.yaml` re-derives just the tag file from an
already-committed manifest, no broker needed. `examples/remote-fleet/scada`
is a complete host project built this way, subscribing to the fleet's
three edge sites (`examples/remote-fleet/sites/*`) — its README ("Proof:
offline and live agree") walks both generation paths side by side, and
`docs/design/examples-dogfood.md` has the walkthrough as actually run.

## Types

`types:` and `tags:` name Sparkplug datatypes. A metric lands in the tag
store as the IEC type of the same width, and `sparkplug_types.st` declares
it that way:

| Sparkplug datatype | ST type |
|---|---|
| Boolean | `BOOL` |
| Int8, Int16, Int32, Int64 | `SINT`, `INT`, `DINT`, `LINT` |
| UInt8, UInt16, UInt32, UInt64 | `USINT`, `UINT`, `UDINT`, `ULINT` |
| DateTime | `LINT`, epoch milliseconds |
| Float, Double | `REAL`, `LREAL` |
| String, Text, UUID | `STRING` |
| Template | the generated `TYPE` |
| an array (Int32Array, BooleanArray, …) | `ARRAY [0..n-1] OF` its element |

The width is documentation: every integer is held as 64 bits and every
real as a double. An array is written as its element plus a length, the
way `eip_manifest.yaml` writes one:

```yaml
types:
  - name: Get_TOD
    fields:
      - { name: EnableIn, type: Boolean }
      - { name: gettime,  type: Int64, arraylen: 7 }    # ARRAY [0..6] OF LINT
tags:
  - { name: W6_Levels, node: W6, metric: Levels, type: Double, arraylen: 3 }
```

The length is the contract. An array metric or template member of any
other length does not fit its binding: it is logged once and refused,
and the tag keeps its last value. It is never truncated or resized. A
template definition does not say how long its array members are, so
`naut sparkplug import` reads the length from an instance in the same
births. If two instances disagree, the import fails. An array member
with no instance in the births, and an empty array, are reported and
left out.

Sparkplug has no array of Templates, and no array of Text or UUID. The
manifest refuses those, along with `type: Int32Array` (write
`type: Int32, arraylen: N`).

Array writes are not supported yet, so an array binding cannot be
`writable:`. The manifest refuses it, and the import binds an array a
`--writable` glob matches as read-only and says so. A writable struct
binding whose type has array members sends its commands without them.
Each array member left out is logged once and counted under
`writes refused` on `/api/drivers`.

## The `__Online`/`__Rebirth` companions

The driver never invents or zeroes a value on death: **Sparkplug's own
semantics are that the last value received *is* the value** until a new
one arrives — the same contract the runtime already has for a driver read
failure. So quality rides on separate, driver-synthesized companions,
declared in the manifest like anything else:

- `<site>__Online` — `BOOL`, true from NBIRTH to the matching NDEATH.
- `<site>_<device>__Online` — the same, per device.
- `<site>__LastBirthMs` — epoch ms of the last NBIRTH.
- `<site>__Rebirth` — `BOOL` **output**; a rising edge sends NCMD Node
  Control/Rebirth to that site — the operator's forced-resync button.

**Tags read zero until first birth — guard on `__Online`.** A tag's data
stays *absent* from the driver's snapshot until the site has birthed at
least once. In the tag store it reads as its `init:`, or the zero of its
type, meanwhile, so a site that is dark when the host starts does not
fault the programs that read its tags (see [Inputs from a device that is
down](/guides/tag-model/#inputs-from-a-device-that-is-down)); its quality
is `NotConnected` until the birth. A zero is not a reading, so interlock
logic gates on `__Online`, which is present from t=0 (`false` until the
first birth): `WellAlarm := W6__Online AND W6_Well_Level > 90.0`, never
`WellAlarm := W6_Well_Level > 90.0` alone.

## Per-tag quality

Beyond `__Online`, the driver implements `io.QualityReporter`: `Quality()`
returns a `map[string]io.Quality` naming, per **data** binding, how much
its value is worth believing right now — non-Good entries only, so a
fully-birthed fleet reports an empty map. A tag never named in it is Good.

- A binding whose metric has **never been delivered** — the site has
  never birthed, or the birth simply never carries that metric — is
  `NotConnected`. The driver cannot tell "hasn't happened yet" from "will
  never happen", so both read the same: nothing to show.
- A binding with a value on file whose site is not currently online — an
  `NDEATH`, a site gone quiet past `StaleAfter`, or (for a device-scoped
  metric) a `DDEATH` on just that device — is `Stale`. The value is real,
  just old.
- A binding whose site is online and has delivered its metric is `Good`
  (omitted).

Only data (read-only) bindings get a verdict. Writable bindings — plain
outputs, member outputs, and the `__Online`/`__LastBirthMs`/`__Rebirth`
companions — are host-authored, not field readings, and are always Good;
`__Online` (etc.) already **is** the quality signal for the tags around
it, so `Quality()` never puts a verdict on top of a verdict.

## Writes: DCMD/NCMD

An output binding's writes leave as `spBv1.0/<group>/NCMD/<edge>` (node
metrics) or `spBv1.0/<group>/DCMD/<edge>/<device>` (device metrics), QoS
0, retain false, full metric names (never aliases).

### Writes to a site that is down

**A command to a dark site is delivered when it comes back, once, unless
the site already reports that value.**

An operator who dials a setpoint on a site showing `__Online: false` has
still asked for something, and the ask outlives the outage: the write is
held per site (the latest value per tag, up to 256 tags) and delivered on
that site's next `NBIRTH`/`DBIRTH` — after the birth has settled what the
site actually holds. If the site comes back already at the commanded
value, the command is satisfied and nothing goes on the wire; if it comes
back at anything else, one command goes out, and only for the members the
operator touched. Write twice while the site is down and the second value
is what lands, because that is what the operator asked for last.

`Status` counts both halves: `WriteQueued` is how many commands have been
parked, `QueuedWrites` how many are waiting *right now* (per site in
`Status.Nodes[].QueuedWrites`, and as a "queued writes" metric on
`/api/drivers`), and `WriteDrops` is reserved for commands that will
never be sent at all — a node no topic addresses, or a site whose queue
is full.

## Writing template members

Most of the controls on a water-utility HMI live *inside* a UDT —
`Motor1.START`, `AnalogInput.HSP`, `LevelControl.LSP`. A Template
metric binds to one struct tag, and writing that struct back as a whole
would clobber every member the site's own logic is driving. So a UDT is
**never writable as a whole** — a `--writable` glob that matches a
Template metric is a hard error — and its controls are bound **per
member** instead:

```yaml
tags:
  - { name: W6_Pump1,       node: W6, metric: Pump1, type: Motor }
  - { name: W6_Pump1_Speed, node: W6, metric: Pump1, type: Motor,
      member: Speed, writable: true }
```

`member:` is a dotted path into the binding's `types:` shape — `Speed`,
or `Drive.Torque` to reach a nested template. The generated tag is a
plain **scalar** carrying the leaf member's type
(`W6_Pump1_Speed : LREAL`), so ST and the HMI bind it like any other
setpoint. Member bindings are **output-only** (`writable: true` is
required): reads keep coming from the metric's own struct tag, which
already carries every member's live value, so a fleet does not pay for a
duplicate input tag per member.

A write publishes a **partial template** — the parent metric's name,
carrying only that member — which Sparkplug B permits in NCMD/DCMD and
which nautilus's own edge merges member by member into its tag store.
Two members of the same UDT written in one coalesce window go out as one
partial template, not two metrics sharing a name. Members the host never
wrote are simply absent from the payload, so the site keeps them.

Generate them with a `--writable` pattern containing a `.`, matched
against `<metric>.<member.path>`:

```bash
naut sparkplug import --broker tcp://mqtt:1883 --group Plant \
  --writable 'PLC1/Pump/SpeedSP,Motor1.START,*.HSP,*.LVL.CTL*SP'
```

Each match generates its own tag, named `<prefix>_<metric>_<member path>`
— `Motor1.START` at site `W6` becomes `W6_Motor1_START : BOOL`. Offline,
a `--sites` file says the same thing with a list instead of `true`:
`{name: Motor1, type: Motor, writable: [START, LVL.HSP]}`.

### Outputs are commands: the baseline rule

**An output that has not moved since the host started is not a command,
and nothing goes on the wire for it.**

This matters more than it sounds. The runtime rebuilds and hands the
driver *every* output on *every* scan, and an output tag's value before
anyone writes it is its `init:` — the zero of its type by default. Take
that at face value and a host connecting to a fleet that is already
online publishes every writable output once, each carrying a zero nobody
asked for. For a member binding that zero is a partial template, and the
edge merges partial templates member by member, so every commissioned
setpoint in the writable set lands on `0` while the members outside it
sit there looking fine. A zeroed span reads as a configuration fault at
the panel; the site is then wrong for the rest of the run.

So the driver reads its output set as follows.

- **The first snapshot after `Start` is the baseline.** It is recorded
  and published to nobody: it describes the world at t=0, it does not
  command it.
- **Thereafter only a value that actually moved is a command**, and it
  is published once, however many scans hand it back afterwards.
- **A member output adopts the site's live value** the moment its parent
  template arrives — NBIRTH, DBIRTH, NDATA or a later rebirth. Dial a
  member to the value the panel already reports and nothing is sent;
  dial it anywhere else and one partial template goes out. Until the
  parent template has arrived a member's baseline is just its `init:`,
  and an operator write before the first birth still publishes.
- **A reconnect replays nothing.** Change detection tracks the runtime's
  output tags, not the broker session, so it survives a dropped
  connection intact. Commands genuinely raised while the broker was away
  are still queued and go out on reconnect.
- **`__Rebirth` follows the same rule.** Only a rising edge is a command,
  and a `__Rebirth` that is already TRUE on the first scan is baseline,
  not an edge: a value held TRUE (or retained) across a restart does not
  ask every site to rebirth each time the controller starts.

None of this depends on how often the runtime calls the driver. The scan
loop hands the driver *every* output on the calls it makes, but it only
makes them when an output actually moved (plus the first scan, a scan
after a failed write, and the first scan after a redundancy takeover).
The rules above are written over the values, not the cadence — and a
command for a site that is down is held rather than retried, so it does
not need a call that will never come.

Host and sites can therefore be started in any order, and a site can be
commanded while it is down.

## `/api/drivers`

`Kind: "sparkplug-host"`, `Name: <host-id>`. State climbs `error` (broker
down) → `connecting` (connected, no births yet) → `degraded` (any node
offline, or unknown metrics under `on-unknown: strict`) → `connected`.
`Devices` carries **one row per edge node** — `{ID: "W6", Online: true,
Detail: "12 tags · born 3m"}` — device sub-rows flattened as `W6/plc1`.
That's the same shape `DriverStatusPanel`/`DriverStatusCard` already
render for an `eip` driver, so a host project's per-site comms status
needs zero HMI changes.

## Redundancy

With a `redundancy:` section (lease-elected standby pair), only the
**leader** may claim to be online: `naut run` hands the elector to the
driver as its write gate, and the driver gates its STATE publish — and
every outbound NCMD/DCMD — on that leadership, not just on
`primary: true`. A standby that published STATE online anyway would flap
its LWT across every site in the fleet on every failover.

- **The standby** connects and consumes the group like the leader, but
  with no STATE will and no STATE birth, and its commands stay queued.
- **On promotion** the new leader reconnects with the will and publishes
  STATE online.
- **On demotion** the old leader stops commanding at once and publishes
  its STATE death before it reconnects as a standby. The death carries its
  own session timestamp, never a newer one, so an edge node that has
  already seen the new leader's birth ignores it. If it lands on the broker
  after that birth anyway, the new leader publishes its birth again, so an
  edge node that connects later still reads STATE online.

Both replicas share the `host-id` (the STATE topic) but not an MQTT
client ID. Under `redundancy:` each replica appends its identity
(`POD_NAME`, or the hostname off-cluster) to `client-id`, so a pair never
takes over each other's broker session. A broker ACL keyed on the client
ID has to allow both.

A project without `redundancy:` has no gate. It is always the leader and
publishes STATE online from its first connect.

## Conformance

`sparkplug/host` passes the **Sparkplug TCK host-application profile** —
CI drives its own edge client (NBIRTH, DBIRTH, NDATA, a deliberate
sequence gap, DDEATH, NDEATH) into the TCK harness alongside the driver
and asserts a non-zero PASS count with zero FAIL, so an all-N/A run
cannot pass silently. Both TCK profiles — edge-node and host-application
— run on every push.

## From Go

The manifest section is the manifest form of the `host` package
(`sparkplug/host`, imported as `sphost`) — a custom topology (a host that
is also an edge, a bespoke discovery policy) uses it directly, as an
`io.Driver` in `runtime.Options`:

```go
import sphost "github.com/joyautomation/nautilus/sparkplug/host"

manifest, _ := sphost.ParseManifest(manifestYAML)
driver, err := sphost.New(manifest, sphost.Config{
    BrokerURL: "tcp://mqtt.plant:1883",
    HostID:    "plant-scada",
    GroupIDs:  []string{"Plant"},
    Primary:   true,
    StateForm: "both",
},
    sphost.WithDiscovery("log"),
)
driver.Start(ctx)

rt, err := runtime.New(runtime.Options{
    Program: program,
    Driver:  driver,
    Inputs:  driver.InputNames(),
    Outputs: driver.OutputNames(),
})
```

`New` never dials — it builds the whole driver (manifest, indexes,
companion tags) offline, so `buildDriver` can run inside `naut check`
and `naut build` with no broker in sight. `Start` owns the connection
and its own reconnect loop; `Stop` publishes the STATE death certificate
before disconnecting, which the TCK requires ahead of both clean and
unclean teardown.
