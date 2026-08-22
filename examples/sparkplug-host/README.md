# sparkplug-host — a plant SCADA consuming a fleet of edge nodes

A manifest project on the **other side of the wire** from
`examples/heated-tank-nogo` or `examples/client60`. Those publish one
controller as a Sparkplug B **edge node**; this one is a Sparkplug B
**host application** — it subscribes to a whole group, presents every
edge node's data as INPUT tags, and sends operator writes back out as
NCMD/DCMD. Shape: `driver: {type: sparkplug-host}` plus
`nautilus sparkplug import`, mirroring `eip` end to end
(`docs/design/sparkplug-host.md`).

```sh
nautilus check .   # compile + verify the generated tags agree with fleet.st
nautilus test -v   # acceptance tests, virtual time, no broker
nautilus run       # connect to driver.broker and go live
```

## Host vs. edge, in one picture

|                | edge node (`sparkplug:` section)             | host application (`driver: {type: sparkplug-host}`) |
|----------------|-----------------------------------------------|-------------------------------------------------------|
| What it is     | one controller's tag store, on the wire       | a consumer of many edge nodes, in one project          |
| Publishes      | NBIRTH/NDATA/NDEATH for **its own** tags      | STATE (its own online/offline certificate)             |
| Subscribes to  | its primary host's STATE (optional)           | every node's NBIRTH/NDATA/NDEATH in the group          |
| Writes arrive as | NCMD/DCMD, applied to its own tags          | sent out as NCMD/DCMD, to a chosen site                |
| Example        | `examples/client60`, `examples/heated-tank-nogo` + a `sparkplug:` block | this project |

A project can in principle be both at once (consume a group *and*
republish it as one aggregate edge node), but that is not this example —
see `docs/design/sparkplug-host.md` §8.8.

## How the generated files were made

Three files are committed, generated, and **never hand-edited**:
`sparkplug_types.st` (the `Motor` Template as an ST `TYPE`),
`sparkplug_manifest.yaml` (node/device/metric bindings, validated against
every birth at runtime), and `tags/sparkplug.yaml` (the tag list —
bindings plus the driver-synthesized `__Online`/`__LastBirthMs`/`__Rebirth`
companions).

**Offline**, from `sites.yaml` — a committed description of the fleet, no
broker required. This is how these files were actually generated, and how
CI regenerates and diffs them:

```sh
go run ./cmd/nautilus sparkplug import --sites sites.yaml --out .
```

`sites.yaml` describes three generic water-system sites: `W6` (a well —
a level transmitter, and one pump as a `Motor` Template instance with a
writable speed setpoint), `BP2` (a booster station — node-level pressure
instruments plus a `plc1` device carrying pump run/fault and its own
setpoint), and `CL1` (a chlorine analyzer — the minimal shape, no device,
no Template).

`W6`'s `Pump1` also shows the **member binding**: `writable: [Speed]` on a
Template metric generates one scalar output tag, `W6_Pump1_Speed : LREAL`,
addressing `Pump1.Speed` inside the UDT. A write publishes an NCMD carrying
a *partial* template — just that member — which the edge merges, so the
members the site drives itself (`Run`) survive. A UDT is never writable as
a whole; that would clobber them. `fleet.st` forwards the operator's
`PumpSpeedCmd` into it, interlocked on `W6__Online`, and `fleet_test.yaml`
covers it.

**Live**, listening to a real broker — the other input to the *same*
generator, so both paths agree on tag names byte-for-byte for the metrics
they share:

```sh
go run ./cmd/nautilus sparkplug import --broker tcp://broker:1883 \
    --group Plant --out .
```

This asks every node it hears from to rebirth (`--rebirth`, default true —
births are not retained, so a mid-stream import sees nothing until it
asks), listens up to `--listen` (default 30s, returns early once the
fleet has settled), and writes the same three files. Re-run either path
any time a site's tag list changes; `git diff` shows exactly what moved.

`nautilus sparkplug tags sparkplug_manifest.yaml` re-derives just the tag
file from an already-committed manifest — no broker, for when the
manifest was hand-edited or a `--tags-skip` pattern changed.

## Reading the fleet: `fleet.st`

One task, `fleet.st`, computes three rollups:

- **`SitesOnline`** — how many of the three sites are currently online,
  counted from `W6__Online`, `BP2__Online`, `CL1__Online`.
- **`AnyPumpRunning`** — `W6_Pump1.Run OR BP2_plc1_Pump_Run`, each ANDed
  with its site's `__Online` — see the guard rule below.
- **`WellLowLevelAlm`** — `W6_Well_Level` below `WellLowLevelSP`, proven
  10 s with a `TON`, and only while `W6__Online` — a `fleet_test.yaml`
  test proves it does *not* fire off a stale last-known reading from a
  dead site.

### The `__Online`/`__Rebirth` companions, and the guard rule

The driver never invents or zeroes a value: **Sparkplug's own semantics
are that the last value received IS the value** until a new one arrives,
matching how the runtime already treats a driver read failure. So on an
`NDEATH`, `W6_Pump1.Run` keeps whatever it last held — it does not reset
to `false`. Quality rides on separate, driver-synthesized companion tags
instead:

- **`<site>__Online`** — `BOOL`, true from NBIRTH to the matching NDEATH.
- **`<site>_<device>__Online`** — the same, per device (`BP2_plc1__Online`).
- **`<site>__LastBirthMs`** — epoch ms of the last NBIRTH.
- **`<site>__Rebirth`** — `BOOL` **output**; a rising edge sends an NCMD
  Node Control/Rebirth to that site. It is the operator's forced-resync
  button — nothing in this project's logic binds it, which is *why*
  `nautilus check` warns about it (see below): it is meant to be driven
  from an HMI or `/api/tags`, not from a program.

**The rule this project's logic follows everywhere, and the one the
guide calls out loudly: guard every read of a site's data on its
`__Online` companion.** A site that has never birthed reads as a fault —
correctly and loudly, the same "reads fault" contract every manifest
project has for an unbound input — so `fleet_test.yaml`'s baseline
`given:` blocks always seed every `__Online` and data tag `fleet.st`
touches before the first scan. `nautilus check` cross-verifies this
project already: `WellAlarm`-style interlocking on an un-birthed site
would fault every scan, loudly, in CI, long before it reaches a field
controller.

## `nautilus check` warnings, explained

```
nautilus check: 2 file(s), 0 with errors, 5 warning(s)
```

The five warnings are every writable tag `fleet.st` does not bind:
`W6__Rebirth`, `BP2__Rebirth`, `CL1__Rebirth` (the operator resync
buttons) and `W6_Pump1_SpeedSP`, `BP2_plc1_Pump_SpeedSP` (setpoints this
example doesn't act on). That is expected — a host project's synthesized
outputs and writable bindings are meant for an HMI or the API, not
necessarily a program — and is the same shape
`cmd/nautilus/check_manifest_test.go`'s `TestCheckSparkplugHostProjectOffline`
fixture asserts. Zero *errors* is the bar that must hold.

## Try it live

Everything above runs with no broker in sight — `nautilus check` and
`nautilus test` never dial (`host.New` never dials; connecting is
`Start`'s job, so CI and a laptop with no plant network both pass). To
watch this project consume a **real** edge node:

1. A local broker — either works:

   ```sh
   docker run --rm -p 1883:1883 eclipse-mosquitto
   # or: docker run --rm -p 1883:1883 -p 18083:18083 emqx/emqx
   ```

2. Run this project against it (`nautilus.yaml` already points
   `driver.broker` at `tcp://127.0.0.1:1883`, `group-id: Plant`):

   ```sh
   NAUTILUS_ADDR=localhost:8081 nautilus run
   ```

3. In a second terminal, make `examples/heated-tank-nogo` publish as an
   edge node into the same broker/group — a **local, uncommitted** edit
   (it's a shared test fixture; see `HANDOFF.md`'s Gotchas — don't commit
   changes to it). Add to its `nautilus.yaml`:

   ```yaml
   sparkplug:
     broker: "tcp://127.0.0.1:1883"
     group-id: Plant
     edge-node: W6
   ```

   then, from `examples/heated-tank-nogo`:

   ```sh
   NAUTILUS_ADDR=localhost:8082 nautilus run
   ```

4. Watch `http://localhost:8081` — the dashboard's **Field drivers**
   panel and `GET /api/drivers` show the `sparkplug-host` driver connect,
   a `W6` row go online, and `W6__Online` flip `true` in the tag table.
   heated-tank-nogo's own tags (`TempC`, `LevelPct`, ...) don't match
   this manifest's metrics (`Well/Level`, `Pump1`, ...) — which is the
   point: `on-unknown: log` means they show up counted in
   `/api/drivers`' `extra.unknown` and logged once each, never silently
   dropped, while `W6` is still tracked as online because
   `sparkplug_manifest.yaml` only needs the edge-node id to match, not
   its metrics. Point `--sites`/`--broker` at a fleet that actually
   carries `Well/Level` etc. to see `W6_Well_Level` itself update.

## `/api/drivers`

`Kind: "sparkplug-host"`, `Name: "plant-scada"` (the `host-id`). State
climbs `error` (broker unreachable) → `connecting` (connected, no births
yet) → `degraded` (any node offline, or unknown metrics under
`on-unknown: strict`) → `connected`. `Devices` carries **one row per edge
node** (`W6`, `BP2`, `CL1`), `BP2/plc1` flattened as a device sub-row —
the same shape `DriverStatusPanel`/`DriverStatusCard` already render, so
a host project's comms status needs zero HMI changes.

## Conformance

`sparkplug/host` passes the **Sparkplug TCK host-application profile**
(`NAUTILUS_TCK=1 go test ./sparkplug/host/ -run TestTCKHostConformance`),
CI-gated on every push alongside the edge-node profile.
