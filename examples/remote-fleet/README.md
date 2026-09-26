# remote-fleet — three Sparkplug B edge sites and a SCADA host

A small, generic water system: two wells (`well-1`, `well-2`) feed a
shared distribution tank; a pressure-boosting station (`booster-1`) pushes
it out to the system. Each site is its own Sparkplug B **edge node**,
standalone — no shared library between them, each deploys alone. One
SCADA host (`scada/`) subscribes to the whole fleet, flags which well the
tank level calls (informational — the operator writes the setpoints, over
NCMD, not `scada` itself), and reads its own local field device directly.
Generic small-water-system pattern; no client names, tag conventions, or
logic — see `examples/lift-station` for the flagship single-project
example this fleet's shape is deliberately *not*: everything here is
small on purpose so four `naut run`s and a broker fit on one laptop.

```
remote-fleet/
  sites/well-1/      one pump, called on rising level with hysteresis
  sites/well-2/      the same pattern, standalone — different setpoints and geometry
  sites/booster-1/   two pumps, a pressure PID, a live Modbus flow meter
  scada/             subscribes to all three, flags which well the tank level calls, its own local Modbus device
  compose.yaml       mosquitto (+ optional postgres) for the bench
```

```sh
docker run --rm -d --name fleet-mosquitto -p 1883:1883 eclipse-mosquitto
# or: docker compose -f compose.yaml up -d mosquitto

# four terminals, from examples/remote-fleet/
naut run sites/well-1      # http://localhost:8091
naut run sites/well-2      # http://localhost:8092
naut run sites/booster-1   # http://localhost:8093
naut run scada             # http://localhost:8080
```

**Needs naut ≥ 0.12.0.** `naut check .`/`naut test .` pass, clean, on
every project in this fleet against the released v0.12.0 CLI — `naut
sparkplug import` (both `--sites` and `--broker`), the `drivers:` list
form, and the `sparkplug-host` driver are already released, not
`main`-only. Nothing here exercises `lib/` composition or the newer
`naut check` diagnostics other examples in this codebase depend on.

## What to open first

- `scada/fleet.st` / `scada/overview.fbd` — the fleet-wide rollups
  (`SitesOnline`, `AnyPumpRunning`, `SystemDemandLps`, `TankLevelPct`) and
  the informational well-dispatch flags. *Open With → Function Block
  Diagram* for `overview.fbd`.
- `scada/sites.yaml` — the committed description of the whole fleet:
  one shared `Pump` Template, three sites, the write path
  (`writable: [SpeedSP]`) — the single input both `naut sparkplug import
  --sites` (offline) and `naut sparkplug import --broker` (live) agree on
  byte-for-byte (see "Proof: offline and live agree" below).
- `scada/fleet.mimic.json` — a P&ID overview of the fleet: two well
  tanks and pumps, the shared distribution tank, Booster 1's two pumps, a
  discharge-pressure gauge and a flow sparkline, piped wells → tank →
  booster → "TO SYSTEM". Right-click → *Open With → Mimic Editor*; live
  against `naut run` in `scada/` with the three sites up.
- `sites/well-1/well.st` — the hysteresis call and the speed clamp, the
  simplest of the three sites' own control.
- `sites/booster-1/pressure.fbd` — the discharge-pressure PID and the lag
  pump's flow-demand call, the one site with a live field device.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| Sparkplug B edge: 3 standalone nodes, store-and-forward, publish classes, a device | `sites/*/nautilus.yaml` `sparkplug:` | [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/) |
| Sparkplug host: one host subscribing to a whole group, NCMD writes, dark-site suppression | `scada/nautilus.yaml` `drivers: [{type: sparkplug-host}]` | [Sparkplug host](https://nautilus.joyautomation.com/guides/sparkplug-host/) |
| `naut sparkplug import`, both `--sites` (offline) and `--broker` (live), agreeing byte-for-byte | `scada/sites.yaml`, `scada/sparkplug_manifest.yaml` | [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/) |
| Modbus TCP: a live flow meter riding a Sparkplug node as a DEVICE, `naut modbus import`/`serve` | `sites/booster-1/devices.yaml`, `modbus_manifest.yaml` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| `drivers:` list form: two drivers, one scan (Sparkplug host + the plant's own Modbus device) | `scada/nautilus.yaml` `drivers:` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| Alarms: one rule expanding across a shared Template + suppression on a dark site | `scada/nautilus.yaml` `alarms:` | [Alarms](https://nautilus.joyautomation.com/guides/alarms/) |
| Historian: a separate daemon polling `/api/state` into Postgres | `compose.yaml`, "The historian" below | [Historian](https://nautilus.joyautomation.com/guides/historian/) |
| Retained state + a two-replica redundancy shape (not run live) | `scada/nautilus.yaml` `retain:`/`redundancy:`, `scada/deploy/` | [Redundancy & retained state](https://nautilus.joyautomation.com/guides/redundancy/) |
| Deployment scaffold: a `Dockerfile` + `k8s.yaml`, adapted from `naut new` | `scada/deploy/` | [Deployment](https://nautilus.joyautomation.com/guides/deployment/) |
| HMI: a P&ID mimic built entirely from the kit's built-in components (no custom `hmi/` app) | `scada/fleet.mimic.json` | [HMI kit](https://nautilus.joyautomation.com/guides/hmi/) |
| Acceptance tests: virtual time, no broker, per site plus the fleet-wide rollups | `*_test.yaml` (per project) | [Testing](https://nautilus.joyautomation.com/reference/testing/) |

## Booster-1's own field device

Every site but `booster-1` is pure bench (`driver: {type: memory}`, no
field build in this fleet). `booster-1` also carries a live Modbus device
(its flow meter); without it, `FM1_FlowLps` reads as a task fault (honest
— "reads fault until connected" is the field-driver contract everywhere
in this codebase). To see the lag pump's flow-demand call actually
respond to something real:

```sh
cd sites/booster-1 && naut modbus serve --manifest modbus_manifest.yaml --listen 127.0.0.1:5021 --ramp
```

## The five story beats

**1. The tag database builds itself from the birth certificates.** Bring
the broker and the three sites up first; `scada`'s `sparkplug_manifest.yaml`
/`tags/sparkplug.yaml` were generated offline from `scada/sites.yaml`
(`naut sparkplug import --sites`), no broker required. A live re-import
against the running fleet is the *other* input to the same generator:

```sh
naut sparkplug import --broker tcp://127.0.0.1:1883 --group Water --out /tmp/water-live
diff -r scada/sparkplug_manifest.yaml /tmp/water-live/sparkplug_manifest.yaml
diff -r scada/sparkplug_types.st /tmp/water-live/sparkplug_types.st
diff -r scada/tags/sparkplug.yaml /tmp/water-live/tags/sparkplug.yaml
```

Unfiltered like this, the diffs are **not** empty — a live edge publishes
its WHOLE tag store, so this also picks up every site's internal tags
(`PumpStarts`, `SimDtS`, the setpoints, ...) that `sites.yaml` never
described, plus per-member `Run`/`SpeedCmd` outputs in place of the
`writable: [SpeedSP]` member write (no `--writable` filter was given
here). That's expected, and not the proof — see "Proof: offline and live
agree" below for the narrowed import that actually reproduces
`sites.yaml` byte-for-byte, `desc:` included.

**2. Store-and-forward survives a broker outage.** Stop the broker for a
minute while `well-1` keeps running; when it comes back, the host
receives the buffered samples, flagged historical, with no gap:

```sh
docker pause fleet-mosquitto
sleep 60
docker unpause fleet-mosquitto
```

Watch `scada`'s dashboard (Field drivers panel) or `well-1`'s own log —
`store-forward: 2000` (`sites/well-1/nautilus.yaml`) buffers up to 2000
data messages through the outage. See "Live walkthrough" below.

**3. A dark site suppresses instead of freezing.** Kill `well-2`
(Ctrl-C its `naut run`): `scada`'s `Well2__Online` drops, `SitesOnline`
goes from 3 to 2, and `well-2`'s alarms (the `Pump` Template's `Fault`
rule) move to Suppressed in `/api/alarms` instead of staying lit on
whatever they last read.

**4. An operator write reaches the site over NCMD.** `Well1_Pump1_SpeedSP`
is a plain generated output tag — nothing in `scada` writes it
automatically (`scada/overview.fbd` only flags which well the tank level
*would* call, informationally; see its own header for why it doesn't also
drive the setpoint). Write it directly:

```sh
curl -X POST localhost:8080/api/tags -d '{"name":"Well1_Pump1_SpeedSP","value":50.0}'
```

and watch `well-1`'s own `Pump1.SpeedSP` (`curl localhost:8091/api/state`)
change — the same partial-template NCMD mechanism the
[Sparkplug host guide](https://nautilus.joyautomation.com/guides/sparkplug-host/)
documents, writing one struct member without touching `Run`/`Fault`/`SpeedHz`.

**5. Leader election and retained state, documented.** `scada/nautilus.yaml`
carries `retain: {}` and `redundancy: {lease: water-scada}`;
`scada/deploy/` (`Dockerfile` + `k8s.yaml`, adapted from `naut new`'s
deploy scaffold) is the two-replica shape that exercises them for real.
Not run here — one replica is the whole bench.

## The historian

```sh
docker compose -f compose.yaml up -d postgres
naut historian -source http://localhost:8080 -db postgres://historian:historian@localhost:5432/historian \
  -tags 'TankLevelPct,SystemDemandLps,Well1_WellLevel,Well2_WellLevel,Booster1_DischargePress' \
  -interval 1s
```

Then `scada/nautilus.yaml`'s `server.historian: "http://localhost:8081"`
(already set) lets the dashboard's trend views query it.

## Proof: offline and live agree

Run with the broker up and all three sites birthed (`--metrics`/`--writable`
narrowed to exactly the contract `sites.yaml` declares — a live edge
publishes its WHOLE tag store, so an unfiltered import also picks up
every site's internal tags (`PumpStarts`, `SimDtS`, ...), which is
expected and not part of this proof):

```sh
naut sparkplug import --broker tcp://127.0.0.1:1883 --group Water \
  --metrics 'WellLevel,Pump1,Pump2,DischargePress,SuctionPress,TankLevel,plc1/FM1_FlowLps' \
  --writable '*.SpeedSP' \
  --out /tmp/water-live
diff scada/sparkplug_types.st     /tmp/water-live/sparkplug_types.st
diff scada/sparkplug_manifest.yaml /tmp/water-live/sparkplug_manifest.yaml
diff scada/tags/sparkplug.yaml     /tmp/water-live/tags/sparkplug.yaml
```

**All three diffs come back empty, `desc:` included** — this platform's
Sparkplug metric Properties carry a description across the wire, so a
`--broker` import recovers it same as the offline `--sites` path. `unit:`
and `desc:` on `sites.yaml`'s metrics are copied verbatim from each
site's own local tags for exactly this reason (so the two paths have the
same string to agree on, not because the wire can't supply one) — see
`scada/sites.yaml`'s comment on the `Booster1` entry. Requires
`booster-1`'s own flow meter live too (`naut modbus serve`, above) — the
Sparkplug DEVICE `plc1` births with zero metrics without it, which alone
would make `plc1/FM1_FlowLps` differ.

## Live walkthrough

Expected output, run with the broker, the three sites, and `booster-1`'s
flow meter all up:

**The fleet comes up and the SCADA sees it.**
`curl localhost:8080/api/state` shows `SitesOnline: 3`, live values
moving (`Well1_WellLevel`, `Well2_WellLevel`, `Booster1_DischargePress`,
`SystemDemandLps`, `TankLevelPct`), and `AnyPumpRunning` following
whichever pumps the bench plants happen to be running.

**The NCMD write path.** `Pump1.SpeedSP` on `well-1` reads `45` (its own
init); `POST /api/tags -d '{"name":"Well1_Pump1_SpeedSP","value":50.0}'`
against `scada`, then `curl localhost:8091/api/state` a moment later
shows `Pump1: {..., "SpeedHz": 50, "SpeedSP": 50}` — the write reached
the site and the pump ramped to it.

**The dark site.** Kill `well-2`'s `naut run`: `scada`'s `/api/state`
shows `Well2__Online: false`, `SitesOnline: 2`; `/api/drivers` shows the
`sparkplug-host` row go `degraded`, `"1 of 3 sites offline"`, `Well2`
listed offline among the node rows; `/api/alarms`'s summary shows its
active count drop and `suppressed` pick up `Well2`'s own pump-fault
definition (plus the chlorine analyser's, if its Modbus source isn't
connected either — the alarm engine treats a disconnected source the
same way: not evaluated).

**Store-and-forward.** `docker pause fleet-mosquitto` for a minute or
more while `well-1` keeps running. `well-1`'s own log warns the
connection is lost, then, on unpause, births again and drains its
buffer — example output:

```
19:59:12 WARN sparkplug: connection lost error="pingresp not received, disconnecting"
19:59:48 INFO sparkplug: born group=Water node=Well1 bdSeq=0 nodeMetrics=13 devices=0
19:59:48 INFO sparkplug: store-forward draining remaining=219
19:59:48 INFO sparkplug: store-forward draining remaining=169
19:59:48 INFO sparkplug: store-forward draining remaining=119
19:59:48 INFO sparkplug: store-forward draining remaining=69
19:59:48 INFO sparkplug: store-forward draining remaining=19
```

`/api/drivers` on `well-1` afterward shows `state: connected`,
`buffered: "0 / 2000"` — fully drained. `scada`'s own log shows the
other side of the same reconnect (`requested rebirth` then `node birth`
for `Well1`), and its driver panel's `seq gaps: 0` — the replay lands
with no gap for the historian to see either.

**The historian recipe** (`naut historian` against Postgres) is the one
piece of this walkthrough that needs `compose.yaml`'s `postgres` service
brought up separately — see "The historian" above. See
`docs/design/examples-dogfood.md` for this walkthrough as actually run,
once, while authoring this project.

## Coverage

Sparkplug B edge (3 nodes, store-and-forward, publish classes, a device),
Sparkplug host, `drivers:` (two drivers on one scan — `scada`), alarm
rules over a shared Template + suppression, historian wiring,
retain/redundancy sections, `naut sparkplug import` both ways, `naut
modbus import`. See each project's own README for what it individually
demonstrates, and `docs/design/examples.md`'s matrix for where this
project sits against the rest of `examples/`.

Tests per site cover their own local control (virtual time, no broker);
`scada/scada_test.yaml` covers the rollups, the dispatch flags, the
dark-site suppression, and the NCMD write path itself — an operator's
write to a generated tag holds until the operator changes it again, with
nothing in this project racing it.
