---
title: Consuming a Sparkplug fleet
description: A manifest project as a Sparkplug B host application — consume a whole group of edge nodes as INPUT tags, write back via NCMD/DCMD, and pass the TCK host-application profile.
---

A `driver: {type: sparkplug-host}` section makes a project the **other
side of the wire** from [the edge-node guide](/guides/sparkplug/): instead
of publishing one controller's tags, it subscribes to a whole Sparkplug B
group and presents every edge node's data as `role: input` tags. Operator
writes go back out as NCMD/DCMD. Shape: a manifest-tier `io.Driver`, plus
`nautilus sparkplug import` to generate what it needs — mirroring
`nautilus eip import` end to end.

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
are required — a project missing one fails `nautilus check`, not
`nautilus run`, so a bad config never reaches the field.

## Generating the manifest: `nautilus sparkplug import`

Three files are generated and committed — never hand-edited:
`sparkplug_types.st` (Sparkplug Templates as ST `TYPE`s, composed
automatically as a library — no entry needed in `nautilus.yaml`),
`sparkplug_manifest.yaml` (node/device/metric bindings, validated against
every birth at runtime), and `tags/sparkplug.yaml` (the tag list, via the
same `internal/tagfile.Render` `nautilus eip import` uses).

Two ways to generate them, producing byte-identical output for metrics
they share:

```sh
# Live: listen to a broker, ask every node heard from to rebirth
# (births aren't retained), generate from what birthed.
nautilus sparkplug import --broker tcp://mqtt.plant:1883 --group Plant

# Offline: from a committed site list — no broker, so CI can build a
# ~60-site central project with none of them online, and review a diff
# before the field work happens.
nautilus sparkplug import --sites sites.yaml --out .
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

`nautilus sparkplug browse --broker ... --group ...` prints what's on the
wire without generating anything; `nautilus sparkplug tags
sparkplug_manifest.yaml` re-derives just the tag file from an
already-committed manifest, no broker needed. `examples/sparkplug-host`
is a complete three-site fleet built this way — its README walks both
generation paths and how to point a real edge node (`heated-tank-nogo` +
a local broker) at it.

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

**Reads fault until first birth — guard on `__Online`.** A tag's data
stays *absent* from the driver's snapshot until the site has birthed at
least once, and reading an absent tag faults the scan — correctly and
loudly, the same contract every manifest project has for an unbound
input. `__Online` itself is present from t=0 (`false` until the first
birth), so interlock logic can gate on it safely before any data has
ever arrived: `WellAlarm := W6__Online AND W6_Well_Level > 90.0`, never
`WellAlarm := W6_Well_Level > 90.0` alone. Keep per-site logic in
per-site tasks where practical — one dead site then faults one task, not
the whole controller.

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
`Motor1.START`, `AnalogInput.HSP`, `LevelControl.CTL1HSP`. A Template
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
nautilus sparkplug import --broker tcp://mqtt:1883 --group Plant \
  --writable 'PLC1/Pump/SpeedSP,Motor1.START,*.HSP,*.LVL.CTL*SP'
```

Each match generates its own tag, named `<prefix>_<metric>_<member path>`
— `Motor1.START` at site `W6` becomes `W6_Motor1_START : BOOL`. Offline,
a `--sites` file says the same thing with a list instead of `true`:
`{name: Motor1, type: Motor, writable: [START, LVL.CTL1HSP]}`.

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
- **`__Rebirth` is unaffected**: its baseline is `false` and only a
  rising edge is a command, which is what it always meant.

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
**leader** may claim to be online: the driver gates its STATE publish —
and every outbound NCMD/DCMD — on `runtime.Coordinator`'s leadership, not
just on `primary: true`. A standby that published STATE online anyway
would flap its LWT across every site in the fleet on every failover; this
is designed in, not bolted on afterward.

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
companion tags) offline, so `buildDriver` can run inside `nautilus check`
and `nautilus build` with no broker in sight. `Start` owns the connection
and its own reconnect loop; `Stop` publishes the STATE death certificate
before disconnecting, which the TCK requires ahead of both clean and
unclean teardown.
