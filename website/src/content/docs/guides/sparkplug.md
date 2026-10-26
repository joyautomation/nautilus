---
title: Publishing to MQTT (Sparkplug B)
description: Expose a controller's tags to any Sparkplug-aware SCADA host — faithful types, report-by-exception, and TCK-conformant birth/death — from one manifest section.
---

A `sparkplug:` section in `nautilus.yaml` republishes the controller's tag
store as a **Sparkplug B edge node**. Any Sparkplug-aware host (Ignition,
or anything else speaking spBv1.0) discovers the whole tag database from
the node's birth certificate — names, types, current values, and each
tag's `unit:` and `desc:` (as the `engUnit` and `documentation` metric
properties Ignition maps onto tag properties, on template members too)
arrive as data, not as configuration you re-enter on the SCADA side.

```yaml
sparkplug:
  broker: tcp://broker:1883          # ssl://host:8883 for TLS
  group-id: Plant
  edge-node: line1                   # default: the project name
  username: line1                    # password is NEVER here: NAUTILUS_MQTT_PASSWORD
  bdseq-file: /var/lib/nautilus/line1.bdseq
  writable: ["*_SP", "Pump*.Speed"]  # what a host may write; omit = read-only
  store-forward: 5000
  flatten-udts: false                # true: UDT members as plain metrics (below)
  default-class: { deadband: 0.5, max-interval: 30s }
  classes:
    fast:   { deadband: 0.1, min-interval: 1s, max-interval: 5s }
    alarms: { every-change: true }
  metric-classes:
    fast: ["PIT_*", "FlowGpm"]
    alarms: ["*_Alm"]
```

`naut run` (and a built binary) starts the node alongside the scan
loop; only `broker` and `group-id` are required. Types map faithfully
(below), and a host can write the tags `writable:` names back via
NCMD/DCMD (a setpoint written in the SCADA host lands in the tag store for
the program to act on). `Node Control/Rebirth` is honored.

## Types

| Tag type | Sparkplug datatype |
|---|---|
| `BOOL` | Boolean |
| any integer (`SINT` … `LINT`, `TIME`, an enumeration) | Int64 |
| `REAL`, `LREAL` | Double |
| `STRING` | String |
| a UDT | Template — its definition in the NBIRTH, each tag an instance |
| `ARRAY [..] OF BOOL` | BooleanArray |
| `ARRAY [..] OF` an integer | Int64Array |
| `ARRAY [..] OF REAL` / `LREAL` | DoubleArray |
| `ARRAY [..] OF STRING` | StringArray |

The runtime holds every integer as 64 bits and every real as a double, so
Int64 and Double are the honest datatypes, for an array's elements as for
a scalar: a Logix `gettime : ARRAY [0..6] OF DINT` publishes as an
Int64Array of seven.

An array can be a tag of its own or a member of a UDT. In a template
definition its member's datatype is the array datatype, with no value,
like every definition member; each instance carries the elements. Arrays
travel in the metric's `bytes_value` as the Sparkplug 3.0 spec lays them
out: packed little-endian numbers, a BooleanArray as a 4-byte
little-endian count followed by its bits packed most-significant first,
and a StringArray as UTF-8 strings each ended by a NUL. Eclipse Tahu's
reference encoder agrees on all three. Report by exception treats an
array like a UDT: when any element moves past its deadband, the whole
array publishes.

Sparkplug has no datatype for an array of UDTs or a multi-dimensional
array. A tag of either kind is left out of the birth, and a UDT member of
either kind is left out of the definition and every instance. Each is
logged once, and the tag's other members and every other tag still
publish.

## UDTs on hosts that don't read Templates

A UDT tag publishes as a Sparkplug Template: its definition in the
NBIRTH, each tag an instance. Templates are standard Sparkplug B, but
some hosts skip Template metrics, and a UDT tag then never arrives at
all. `flatten-udts: true` publishes each member as a plain metric of its
own instead, named by its path with `/` between levels. Hosts show that
as a folder tree:

| `flatten-udts: false` (default) | `flatten-udts: true` |
|---|---|
| `P101`, a Template instance of `Motor` | `P101/Running` Boolean, `P101/Speed` Double |
| the `Motor` definition in the NBIRTH | no definitions |
| a nested UDT as a Template member | `P101/Drive/Torque`, at any depth |

The UDT member's `unit:` and `desc:` carry over to its metric. A `desc:`
on the UDT tag itself has nowhere to go and is left out. Report by
exception still decides per tag: the class and deadband that cover
`P101` cover all of its members. When `P101` publishes, a data message
carries only the members that moved since the last publish. A
`max-interval` heartbeat carries all of them. A host writes a member
back under the same name (`P101/Speed`), and it lands in the tag store
as `P101.Speed`. `writable:` patterns name that member path (`P101.Speed`,
`P101.*`), not the published name.

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

## Devices and connection health

Every field connection is its own Sparkplug **device** under the edge
node, and its DBIRTH/DDEATH follow that connection's health: when one
meter's link drops, the host sees that device die — not stale values
pretending to be fresh — while every other device stays born.

| Driver | One device per | Device id |
|---|---|---|
| `modbus`, `snmp`, `redfish`, `prometheus` | source | the source's `id` |
| `eip`, `sparkplug-host`, `replay` | driver | the driver's `name:` (default: its type) |

A device carries the input tags its connection delivers, its `__Online`
companion included. Everything else — computed tags, program state,
outputs, and every tag of the `memory` loopback, which is no connection —
publishes at node level. Two connections with one id would share a topic,
so that is a load error naming both; give a driver a `name:` to choose
its id.

## Writes

The node is **read-only by default**: an NCMD/DCMD that writes a tag is
refused unless `writable:` names it. Anyone who can publish to the broker
can send a command, so this is what stands between the broker and a
controller. `Node Control/Rebirth` is always honored.

```yaml
sparkplug:
  writable:
    - LevelSP               # one tag
    - "Pump*.SpeedSP"       # one member of every Pump* UDT tag
    - Mixer                 # a UDT tag: every member
```

Patterns are globs over the tag name or a UDT member's dotted path; one
that matches a tag opens all of its members. A template command carrying
a writable member and a read-only one writes the first and refuses the
second. A command must also arrive where the tag is published: a DCMD
writes only its own device's tags, an NCMD only node-level ones. Array
writes are not supported yet: a command carrying an array metric, or a
template command with an array member, has the array refused. The
template's other members still apply. Every refusal is logged once per
tag and counted as `commandsRefused` in the node's status.

## Primary host and store-and-forward

Set `primary-host` to a SCADA host's id and the node coordinates with that
host's STATE certificate: it defers its own birth until the host is
ONLINE (both the 2.x and 3.0 STATE encodings are accepted).
`primary-host-offline` says what it does when the host goes offline:

- `buffer`, the default: the node logs a warning and stays connected and
  born. It sends no NDEATH, and it stops publishing NDATA and DDATA until
  the host is back. With `store-forward` that data is buffered and
  replayed as historical once the host returns; without it, it is not
  sent.
- `disconnect`, the Sparkplug 3.0 behaviour: the node publishes NDEATH
  and disconnects, connects again with the next bdSeq, and births only
  once the host is ONLINE again. With `store-forward` it buffers while it
  waits and replays the data as historical after the new birth, as it
  does after a broker outage.

```yaml
sparkplug:
  primary-host: ScadaHost
  primary-host-offline: disconnect   # default: buffer
  store-forward: 5000
```

In `disconnect` mode the NDEATH is published before the MQTT DISCONNECT,
because a clean disconnect makes the broker discard the will; it carries
the ending session's bdSeq. An offline STATE whose timestamp is older than
the last online one is ignored, as the spec requires.

`store-forward: 5000` buffers up to that many data messages while the
broker is unreachable (or the primary host is offline) and replays them,
marked historical, once delivery resumes — so a gap in connectivity is not
a gap in the host's historian. The buffer is a bounded ring: when full,
the oldest record drops so recent data always survives, and the drain is
rate-limited so a backlog doesn't flood the broker on reconnect.

The buffer lives in memory, so by default a process that exits during an
outage (restarted to fix it, upgraded, crashed) loses it. Set
`store-forward-file` to keep it on disk:

```yaml
sparkplug:
  store-forward: 5000
  store-forward-file: /var/lib/nautilus/storeforward.log
```

Each record is appended to the file as it is buffered. On start, a
backlog the last process left there is loaded and replayed, marked
historical and with its original timestamps, after the node's first birth
(after the primary host is ONLINE, when there is one); once it has gone
out the file is cleared. A relative path is relative to the working
directory, like `bdseq-file`.

- **Durability.** Each record is one write to the file as it is buffered,
  so any process exit, `kill -9` included, keeps it. The file is synced to
  disk at most once a second while buffering (and on stop), which keeps a
  long outage from costing a disk flush per tick; a power loss can lose
  the last second of records.
- **At least once.** Records a replay sent just before the process exited
  can be sent again by the next one, as historical, with the same
  timestamps.
- **Bounded.** The file holds at most about twice the ring and loads as
  the newest `store-forward` records, as the ring would have kept.
- **Damage.** A torn last record (a crash mid-write) is skipped with a log
  line. A file that is not a store-forward file, or is damaged part-way,
  is renamed aside to `<file>.corrupt-<time>` and logged, and the node
  starts with what it could read. A file it cannot create or write at
  start (a missing directory, permissions) fails the start.

A replayed backlog carries the metric names it was buffered with: a tag
renamed between the two runs replays under its old name.

`bdseq-file` persists the birth-death sequence number across restarts, as
the spec expects; without it bdSeq restarts at 0 each run.

A dropped broker connection is a new session. The broker publishes the
node's will, an NDEATH with the old bdSeq; the node connects again with
the next bdSeq (retrying every 5 seconds until the broker accepts), writes
it to `bdseq-file`, and births with it. Its new will carries the same
bdSeq as its new NBIRTH, so a host applies the next death and discards a
will from the old connection that arrives late, after the new birth.
Commands, the primary host's STATE and store-and-forward carry on across
the reconnect as above.

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
    sparkplug.WithStoreForwardFile("/var/lib/nautilus/line1.sf"), // omit: memory only
    sparkplug.WithWritable("LevelSP", "Pump*.SpeedSP"), // omit: read-only
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
MQTT and protobuf live only in this package (protobuf also in the PLCnext
driver's generated gRPC stubs, for the firmware's data-access service).
The runtime core's one
dependency outside the Go standard library is `gopkg.in/yaml.v3`, which it
uses to read the project manifest.

## The other side of the wire

Everything above makes a project an edge node — one controller, publishing
its own tags. A project can also be a Sparkplug B **host application**:
`driver: {type: sparkplug-host}` subscribes to a whole group and presents
every edge node in it as `role: input` tags in one central project, with
writes going back out as NCMD/DCMD. See
[Consuming a Sparkplug fleet](/guides/sparkplug-host/).
