# water-scada — the fleet's SCADA host

A plant SCADA on the **other side of the wire** from `../sites`: it
subscribes to the whole `Water` Sparkplug group and presents every site's
data as INPUT tags, sends operator writes back out as NCMD, and reads its
own local field device (a chlorine analyser) directly over Modbus — two
drivers, one scan. See `../README.md` for the fleet story.

```sh
naut check .   # compile + verify the generated tags agree with fleet.st
naut test  .   # acceptance tests, virtual time, no broker
naut run   .   # connect to the broker + the analyser and go live
```

**Needs naut ≥ 0.12.0.** `naut check .`/`naut test .` pass clean on the
released CLI — the `sparkplug-host` driver, the `drivers:` list form
(two drivers, one scan), and both `naut sparkplug import` paths are
already released. See `../README.md` for the fleet-wide version check.

## What to open first

- `fleet.st` — the fleet-wide rollups (`SitesOnline`, `AnyPumpRunning`,
  `SystemDemandLps`, `TankLevelPct`), guarded on each site's own
  `__Online`.
- `overview.fbd` — the informational well-dispatch flags
  (`Well1Called`/`Well2Called`), by tank level. *Open With → Function
  Block Diagram*.
- `sites.yaml` — the committed description of the whole fleet: one
  shared `Pump` Template, three sites, the write path.
- `fleet.mimic.json` — a P&ID overview of the fleet, built entirely from
  the HMI kit's built-in components (`Tank`, `Pump`, `Gauge`,
  `Sparkline`) — no custom `hmi/` app in this project. Right-click →
  *Open With → Mimic Editor*; live against `naut run .` with the three
  sites up. Its `Pump` binds (`<site>_Pump<n>.Run`/`.SpeedHz`) address a
  UDT member the way `naut sparkplug import`'s own docs and the write
  path do, dotted — see `docs/design/examples-dogfood.md` for whether a
  mimic equipment `bind` can actually resolve that dotted path today.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| Sparkplug host: subscribing to a whole group, presenting every site as inputs, NCMD writes | `nautilus.yaml` `drivers: [{type: sparkplug-host}]` | [Sparkplug host](https://nautilus.joyautomation.com/guides/sparkplug-host/) |
| `naut sparkplug import`, offline (`--sites`) and live (`--broker`), agreeing byte-for-byte | `sites.yaml`, `sparkplug_manifest.yaml` | [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/) |
| `drivers:` list form: two drivers, one scan (the fleet + the plant's own Modbus analyser) | `nautilus.yaml` `drivers:` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| Alarms: one rule expanding across a shared Template, `enable:` suppression on a dark site | `nautilus.yaml` `alarms:` | [Alarms](https://nautilus.joyautomation.com/guides/alarms/) |
| Historian: `server.historian` pointing the dashboard at a separate daemon | `nautilus.yaml` `server.historian` | [Historian](https://nautilus.joyautomation.com/guides/historian/) |
| Retained state + a two-replica redundancy shape, deploy scaffold | `nautilus.yaml` `retain:`/`redundancy:`, `deploy/` | [Redundancy & retained state](https://nautilus.joyautomation.com/guides/redundancy/) |
| HMI: a P&ID mimic from the kit's built-ins alone | `fleet.mimic.json` | [HMI kit](https://nautilus.joyautomation.com/guides/hmi/) |
| Acceptance tests: the rollups, the dispatch flags, dark-site suppression, the NCMD write path | `scada_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |

## How the generated files were made

Four files are committed, generated, and **never hand-edited**:
`sparkplug_types.st` (the `Pump` Template as an ST `TYPE`),
`sparkplug_manifest.yaml` and `tags/sparkplug.yaml` (from `sites.yaml`),
and `modbus_manifest.yaml`/`tags/modbus.yaml` (from `devices.yaml`).

**Sparkplug, offline**, from `sites.yaml` — a committed description of the
fleet, no broker required. This is how these files were actually
generated, and how CI regenerates and diffs them:

```sh
naut sparkplug import --sites sites.yaml --out .
```

`sites.yaml` declares ONE Template, `Pump` (`Run`/`Fault`/`SpeedHz`/
`SpeedSP`), shared by all three sites — every pump in this fleet, well or
booster, is the same shape on the wire, even though each site's own
project (`../sites/*/types.st`) carries its own copy of the `TYPE`, not a
shared library.

**Sparkplug, live**, listening to the real fleet — the other input to
the *same* generator, so both paths agree on tag names byte-for-byte:

```sh
naut sparkplug import --broker tcp://127.0.0.1:1883 --group Water --out /tmp/live-import
diff -r . /tmp/live-import   # only the *_test.yaml / README / deploy files differ
```

See the fleet README for this diff actually run against the three live
sites, with the broker up.

**Modbus**, from `devices.yaml` — the chlorine analyser, this project's
own local field device (not a Sparkplug site):

```sh
naut modbus import --map devices.yaml
```

## Reading the fleet: `fleet.st` and `overview.fbd`

`fleet.st` computes:

- **`SitesOnline`** — how many of the three sites are currently online,
  counted from `Well1__Online`, `Well2__Online`, `Booster1__Online`.
- **`AnyPumpRunning`** — any of the fleet's four pumps (`Well1_Pump1`,
  `Well2_Pump1`, `Booster1_Pump1`, `Booster1_Pump2`), each guarded on its
  own site's `__Online`.
- **`SystemDemandLps`** — Booster 1's discharge flow meter
  (`Booster1_plc1_FM1_FlowLps`), guarded on the *device's own*
  `Booster1_plc1__Online` — a Modbus device attached to a Sparkplug node
  has its own online companion, distinct from the node's.
- **`TankLevelPct`** — the distribution tank level, read at Booster 1;
  guarded the same way, reading 0 (not the last-known level) once
  Booster 1 goes dark — deliberately conservative, consistent with "never
  trust a dead site's last value."
- **`ChlorineResidualLowAlm`** — the plant's own local analyser reading,
  no `__Online` to guard on: a disconnected Modbus source just holds its
  last-read value, the ordinary field-driver contract.

`overview.fbd` flags which well(s) the tank level calls: `Well1Called`
(the fixed lead) whenever the tank is below `TankHighSP`; `Well2Called`
(the fixed backup) once it drops further, below `TankLowSP` — both
guarded on the target well's own `__Online` (a dark well can't
meaningfully be "called"). These are informational only: `overview.fbd`
deliberately does **not** also write `Well1_Pump1_SpeedSP`/
`Well2_Pump1_SpeedSP` — those generated NCMD tags are the fleet's
operator write path (story beat 4), and a program racing it every scan
would just fight the operator's own write on the very next tick. Writing
`Well1_Pump1_SpeedSP` (an ordinary POST /api/tags, or an operator's own
call) reaches `../sites/well-1`'s `Pump1.SpeedSP` and holds there —
`scada_test.yaml` proves it sticks in virtual time; the fleet README's
live walkthrough does it from `/api/tags` and watches the site.

### The `__Online` guard rule

Same rule `examples/sparkplug-host` calls out loudly, extended to the
plant's own local device: **guard every read of a site's (or a device's)
data on its own online companion.** `scada_test.yaml`'s baseline `given:`
blocks always seed every `__Online` and data tag `fleet.st`/`overview.fbd`
touch before the first scan — dropping any one of them reproduces exactly
the fault the guard rule exists to prevent.

## Alarms: one rule, three sites

```yaml
rules:
  - match: { type: Pump, member: Fault }
    name: "{desc} fault"
    priority: high
    on-delay: 1s
    enable: "{site}__Online"
    class: equipment
```

`type: Pump` is the shared Template — this ONE rule expands to four
definitions, one per pump on the wire (`Well1_Pump1.Fault`,
`Well2_Pump1.Fault`, `Booster1_Pump1.Fault`, `Booster1_Pump2.Fault` — see
`naut alarms list .`), each `{desc}` filled from that metric's own
`desc:` in `sites.yaml`, each `enable:` interlocked on its
own site's `__Online`: a site going dark moves its alarms to Suppressed
instead of freezing them lit (`scada_test.yaml` proves the transition and
the resumption). `TankLow`, `ResidualLow` and `AnalyzerFault` are
hand-written `defs:` — nothing on the wire carries them as a Template
member, so there's no rule to write them as.

## `naut check` warnings, explained

```
naut check: 3 file(s), 0 with errors, 7 warning(s)
```

The seven warnings are every writable tag this project does not bind:
`Well1_Pump1_SpeedSP`, `Well2_Pump1_SpeedSP`, `Booster1_Pump1_SpeedSP`,
`Booster1_Pump2_SpeedSP` (every pump's speed setpoint — these are the
fleet's operator/NCMD write path, meant for `/api/tags` or an HMI, not a
program; see "Reading the fleet" above for why `overview.fbd`
deliberately leaves them alone) and `Well1__Rebirth`/`Well2__Rebirth`/
`Booster1__Rebirth` (the operator's forced-resync buttons, same
rationale). Zero *errors* is the bar that must hold — same shape
`examples/sparkplug-host`'s README documents for its own five warnings.

## Redundancy, retain, and the historian

`retain: {}` persists every setpoint (`TankHighSP`/`TankLowSP`/
`ResidualLowSP`, every alarm limit, every operator-written pump speed
setpoint) across a restart —
`file: retain.json` by default. `redundancy: {lease: water-scada}` is the
shape a two-replica deployment needs: only the elected leader's drivers
dial out or publish; a standby answers the API by proxying to the leader.
Neither is exercised live in this bench walkthrough (one replica), but
`deploy/k8s.yaml` is the scaffold that would run it for real — adapted
from `naut new`'s deploy template, documented here, not applied.

`server.historian: "http://localhost:8081"` points the dashboard's
history views at a separate `naut historian` daemon (Postgres-backed,
polling `/api/state`) — see the fleet README for the recipe. Harmless
with nothing listening: `/history*` just proxies an error.

## Try it live

See `../README.md` for the full walkthrough (the broker, the three
sites, the dispatch, the dark-site demo, store-and-forward). This
project alone:

```sh
docker run --rm -d --name fleet-mosquitto -p 1883:1883 eclipse-mosquitto
naut run .
curl localhost:8080/api/state | jq '.SitesOnline, .TankLevelPct'
```
