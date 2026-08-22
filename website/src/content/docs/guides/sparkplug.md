---
title: Publishing to MQTT (Sparkplug B)
description: Expose a controller's tags to any Sparkplug-aware SCADA host — faithful types, report-by-exception, and TCK-conformant birth/death — from one manifest section.
---

A `sparkplug:` section in `nautilus.yaml` republishes the controller's tag
store as a **Sparkplug B edge node**. Any Sparkplug-aware host (Ignition,
or anything else speaking spBv1.0) discovers the whole tag database from
the node's birth certificate — names, types, and current values arrive as
data, not as configuration you re-enter on the SCADA side.

```yaml
sparkplug:
  broker: tcp://broker:1883          # ssl://host:8883 for TLS
  group-id: Plant
  edge-node: line1                   # default: the project name
  username: line1                    # password is NEVER here: NAUTILUS_MQTT_PASSWORD
  bdseq-file: /var/lib/nautilus/line1.bdseq
  device: plc1                       # the field driver's tags as a Sparkplug DEVICE
  store-forward: 5000
  default-class: { deadband: 0.5, max-interval: 30s }
  classes:
    fast:   { deadband: 0.1, min-interval: 1s, max-interval: 5s }
    alarms: { every-change: true }
  metric-classes:
    fast: ["PIT_*", "FlowGpm"]
    alarms: ["*_Alm"]
```

`nautilus run` (and a built binary) starts the node alongside the scan
loop; only `broker` and `group-id` are required. Types map faithfully —
BOOL→Boolean, integers→Int64, REAL→Double, UDT→Template — and a host can
write tags back via NCMD (a setpoint written in the SCADA host lands in
the tag store for the program to act on). `Node Control/Rebirth` is
honored.

## Publish classes

Publish classes are report-by-exception groups, the Sparkplug analogue of
scan classes: `metric-classes` assigns tags to a named class by glob, and
everything unassigned uses `default-class`. Each class tunes when a change
is worth publishing:

- **`deadband`** — publish a numeric only when it moves more than this in
  absolute units. Zero publishes on any change.
- **`min-interval`** — never publish more often than this, even on change
  (debounce for noisy signals).
- **`max-interval`** — publish at least this often even when unchanged
  (heartbeat, so flat lines stay visibly alive).
- **`every-change: true`** — every transition publishes unconditionally,
  ignoring deadband and rate limits — for tags where each edge matters
  (alarms, counters). Unchanged samples still stay quiet.

## Devices and driver health

`device: plc1` publishes the field driver's input tags as a Sparkplug
**device** under the edge node, and its DBIRTH/DDEATH follow the driver's
own connection health: when the EtherNet/IP link drops, the host sees the
device die — not stale values pretending to be fresh. Everything else
(computed tags, program outputs) publishes at node level. Omit `device`
and the whole tag store publishes at node level.

## Primary host and store-and-forward

Set `primary-host` to a SCADA host's id and the node coordinates with that
host's STATE certificate, per the spec: it defers its own birth until the
host is ONLINE, and dies (NDEATH) when the host goes offline — both the
2.x and 3.0 STATE encodings are accepted.

`store-forward: 5000` buffers up to that many data messages while the
broker is unreachable (or the primary host is offline) and replays them,
marked historical, once delivery resumes — so a gap in connectivity is not
a gap in the host's historian. The buffer is a bounded ring: when full,
the oldest record drops so recent data always survives, and the drain is
rate-limited so a backlog doesn't flood the broker on reconnect.

`bdseq-file` persists the birth-death sequence number across restarts, as
the spec expects; without it bdSeq restarts at 0 each run.

## From Go

The manifest section is the manifest form of the `sparkplug` package —
custom drivers and richer topologies use it directly:

```go
node, _ := sparkplug.New(rt, sparkplug.Config{
    BrokerURL: "tcp://broker:1883", GroupID: "Plant", EdgeNode: "line1",
    BdSeqFile: "/var/lib/nautilus/line1.bdseq",
},
    sparkplug.WithDefaultRBE(sparkplug.RBE{Deadband: 0.5, MaxInterval: 30 * time.Second}),
    sparkplug.WithPublishClass("fast", sparkplug.RBE{Deadband: 0.1, MaxInterval: 5 * time.Second}),
    sparkplug.WithMetricClass("fast", "PIT_*"),
    sparkplug.WithStoreForward(5000),
    // Any io.Driver's tags can be a device; DBIRTH/DDEATH track its health.
    sparkplug.WithDevice(sparkplug.Device{
        ID: "plc1", Tags: driver.InputNames(),
        Health: func() bool { return driver.Health().Connected },
    }),
)
node.Start(ctx)
```

## Conformance

The node passes the **Sparkplug TCK edge-node profile** — CI runs the
`joyautomation/sparkplug-tck-go` harness against a live node on every push.
MQTT and protobuf live only in this package; the runtime core stays
stdlib-only.

## The other side of the wire

Everything above makes a project an edge node — one controller, publishing
its own tags. A project can also be a Sparkplug B **host application**:
`driver: {type: sparkplug-host}` subscribes to a whole group and presents
every edge node in it as `role: input` tags in one central project, with
writes going back out as NCMD/DCMD. See
[Consuming a Sparkplug fleet](/guides/sparkplug-host/).
