# remote-fleet — three Sparkplug B edge sites and a SCADA host

A small, generic water system: two wells (`well-1`, `well-2`) feed a
shared distribution tank; a pressure-boosting station (`booster-1`) pushes
it out to the system. Each site is its own Sparkplug B **edge node**,
standalone — no shared library between them, each deploys alone. One
SCADA host (`scada/`) subscribes to the whole fleet, dispatches the wells
by tank level, and reads its own local field device directly. Generic
small-water-system pattern; no client names, tag conventions, or logic —
see `examples/lift-station` for the flagship single-project example this
fleet's shape is deliberately *not*: everything here is small on purpose
so four `naut run`s and a broker fit on one laptop.

```
remote-fleet/
  sites/well-1/      one pump, called on rising level with hysteresis
  sites/well-2/      the same pattern, standalone — different setpoints and geometry
  sites/booster-1/   two pumps, a pressure PID, a live Modbus flow meter
  scada/             subscribes to all three, dispatches the wells, its own local Modbus device
  compose.yaml       mosquitto (+ optional postgres) for the bench
```

Needs **naut ≥ 0.13.0** (`drivers:` list, the Sparkplug host driver, and
`naut sparkplug import --sites` are all recent additions — `lib/`
composition and the newer `naut check` messages referenced elsewhere in
this codebase are main-branch-only and not exercised here).

## Run it all on one laptop

```sh
docker run --rm -d --name fleet-mosquitto -p 1883:1883 eclipse-mosquitto
# or: docker compose -f compose.yaml up -d mosquitto

# four terminals, from examples/remote-fleet/
naut run sites/well-1      # http://localhost:8091
naut run sites/well-2      # http://localhost:8092
naut run sites/booster-1   # http://localhost:8093
naut run scada             # http://localhost:8080
```

`booster-1` also carries a live Modbus device (its flow meter); without
it, `FM1_FlowLps` reads as a task fault (honest — "reads fault until
connected" is the field-driver contract everywhere in this codebase). To
see the lag pump's flow-demand call actually respond to something real:

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

Empty diffs on `sparkplug_manifest.yaml`/`sparkplug_types.st` (the `--broker`
path cannot recover a metric's `desc:` from the wire — see
`examples/sparkplug-host`'s README — so `tags/sparkplug.yaml`'s `desc:`
fields are the one expected difference; every tag *name*, *role*, *type*
and `writable:` still matches exactly). See "Proof: offline and live
agree" below for this actually run.

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
data messages through the outage. See "Live evidence" below.

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
curl -X POST localhost:8080/api/tags -d '{"Well1_Pump1_SpeedSP": 50.0}'
```

and watch `well-1`'s own `Pump1.SpeedSP` (`curl localhost:8091/api/state`)
change — the same partial-template NCMD mechanism
`examples/sparkplug-host` documents, writing one struct member without
touching `Run`/`Fault`/`SpeedHz`.

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
(already set) lets the dashboard's trend views query it. See "Live
evidence" below for whether this was actually run in this session.

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

Actually run in this session: **all three diffs are empty.** `unit:` and
`desc:` on `sites.yaml`'s metrics are copied verbatim from each site's own
local tags for exactly this reason — see `scada/sites.yaml`'s comment on
the `Booster1` entry.

## Live evidence

All run in this session, mosquitto on an alternate port (1883 was taken
by another broker on the machine — same recipe, `-p <port>:1883`):

**The fleet comes up and the SCADA sees it.**
`curl localhost:8080/api/state` showed `SitesOnline: 3`, live values
moving (`Well1_WellLevel`, `Well2_WellLevel`, `Booster1_DischargePress`,
`SystemDemandLps`, `TankLevelPct`), and `AnyPumpRunning: true`.

**The NCMD write path.** `Pump1.SpeedSP` on `well-1` read `45` (its own
init); `POST /api/tags -d '{"name":"Well1_Pump1_SpeedSP","value":50.0}'`
against `scada`, then `curl localhost:8091/api/state` a moment later
showed `Pump1: {..., "SpeedHz": 50, "SpeedSP": 50}` — the write reached
the site and the pump ramped to it.

**The dark site.** Killed `well-2`'s `naut run`; `scada`'s
`/api/state` showed `Well2__Online: false`, `SitesOnline: 2`;
`/api/drivers` showed the `sparkplug-host` row go `degraded`, `"1 of 3
sites offline"`, `Well2` listed offline among the node rows;
`/api/alarms`'s summary showed `suppressed: 2` (`Well2`'s own pump-fault
definition, plus the chlorine analyser's — its Modbus source was never
connected in this session either, which the alarm engine treats the same
way: not evaluated).

**Store-and-forward.** `docker pause fleet-mosquitto` for 65 s while
`well-1` kept running. `well-1`'s own log:

```
19:59:12 WARN sparkplug: connection lost error="pingresp not received, disconnecting"
19:59:48 INFO sparkplug: born group=Water node=Well1 bdSeq=0 nodeMetrics=13 devices=0
19:59:48 INFO sparkplug: store-forward draining remaining=219
19:59:48 INFO sparkplug: store-forward draining remaining=169
19:59:48 INFO sparkplug: store-forward draining remaining=119
19:59:48 INFO sparkplug: store-forward draining remaining=69
19:59:48 INFO sparkplug: store-forward draining remaining=19
```

`/api/drivers` on `well-1` afterward showed `state: connected`,
`buffered: "0 / 2000"` — fully drained. `scada`'s own log shows the
other side of the same reconnect (`requested rebirth` then `node birth`
for `Well1`), and its driver panel's `seq gaps: 0` — the replay landed
with no gap for the historian to see either.

**The historian recipe** (`naut historian` against Postgres) was
**not** run live in this session — `compose.yaml`'s `postgres` service
and the recipe above are documented, not exercised.

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
