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
      - { name: Pump1,      type: Motor }              # a Template instance
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
0, retain false, full metric names (never aliases). Writes to an offline
node are dropped and counted (`Status.WriteDrops`) rather than queued — a
command to a dark site is meaningless, and the operator is already seeing
`__Online: false`.

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
