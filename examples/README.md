# examples — four plants, not four feature demos

Four small, believable control projects that between them cover everything
nautilus supports: every IEC 61131-3 language, every field driver, alarms,
historian, retain, redundancy, online edits, and an HMI mimic. None of them
is a client's plant — generic water/wastewater, batching, and remote-site
patterns, deliberately small so they run on a laptop with no hardware.

## Run an example and see its HMI

New to nautilus? Do the [tutorial](../docs/tutorial.md) first. It installs
everything and builds a small project from scratch. This section is the
next step: run a finished project from this folder and open its screen.

Every example is a normal nautilus project. You run it from its own folder.
The example here is `lift-station`. The other examples work the same way.

**You need:** the `naut` command ([tutorial, Part 1](../docs/tutorial.md#part-1-install))
and a copy of this repository. Get the copy one of two ways:

- With git: `git clone https://github.com/joyautomation/nautilus.git`
- Without git: on the repository page, click *Code*, then *Download ZIP*,
  and unzip it.

For the operator screen you also need [Node.js](https://nodejs.org/) 20 or
newer (the LTS installer is fine).

**1. Run the controller (no Node needed).** In a terminal:

```sh
cd nautilus/examples/lift-station
naut run .
```

You should see a line containing `tag API on http://localhost:8080`. Before
it, a line says `hmi/build is not built yet` and that the built-in
dashboard is served instead. That is expected until step 2. Open <http://localhost:8080>. You see the engineering dashboard: the
scan diagnostics and a table of live tags. The water level (`LIT101_Level`)
moves because the project simulates the plant. To stop, press `Ctrl+C` in
the terminal.

**2. Build the operator screen (needs Node).** The screen is a small web
app in the example's `hmi/` folder. It is built from the repository's own
`hmi/` and `hmi-3d/` packages, so build those once first. Stop the
controller, then run these from `nautilus/examples/lift-station`:

```sh
cd ../../hmi
npm install
npm run package
cd ../hmi-3d
npm install
npm run package
cd ../examples/lift-station/hmi
npm install
npm run build
cd ..
```

Each `npm install` downloads packages and can take a few minutes. A
warning about `allow-scripts` is harmless. The last command ends with
`Wrote site to "build"`.

**3. Run it again.**

```sh
naut run .
```

The first line now says `hmi (hmi/build)`. Open <http://localhost:8080>.

You should see the 2D screen (the P&ID): the wet well with its level, two
pumps, the check valves, an alarm banner, a faceplate for each pump, the
level setpoints you can edit, and a level trend. The engineering dashboard
moves to <http://localhost:8080/_nautilus/>.

**4. See the 3D view.** Open <http://localhost:8080/3d>. It is the same
station in 3D. Drag with the mouse to orbit, scroll to zoom, and click a
pump or the well to see its values. To see it drawn as a line drawing,
add `?look=paper`: <http://localhost:8080/3d?look=paper>.

**5. Make something happen.** The inflow to the well is a setpoint. In the
dashboard at <http://localhost:8080/_nautilus/>, find `InflowLps` and set it
to `60`. This is the "storm" case. In about a minute the well fills past
the lag level and the second pump starts. You can watch it on both the 2D
screen and the 3D view.

**Other examples.** `spatial-rig`, `well-site` and `node-3d` also have an
`hmi/` folder (3D screens). Open each project's own README for its steps.
`batch-skid` and `remote-fleet` have a mimic file but no built screen. Open
the mimic in VS Code (right-click, *Open With*, *Mimic Editor*). Note that
`remote-fleet` is four projects, each started from its own folder.

**Develop the screen.** `npm run dev` in the `hmi/` folder serves the screen
at <http://localhost:5173> and reloads on every save. Keep `naut run .`
running in another terminal.

**If it does not work.**

- *The page says "not built yet" or `naut run` prints that `hmi/build` is
  not built yet:* run step 2.
- *`npm run build` fails with "Cannot find module" for
  `@joyautomation/nautilus-hmi`:* the `npm run package` steps in `hmi/` and
  `hmi-3d/` have not been run. Run step 2 from the top.
- *Port 8080 is busy:* run `NAUTILUS_ADDR=localhost:8090 naut run .`
  (Windows PowerShell: `$env:NAUTILUS_ADDR="localhost:8090"; naut run .`)
  and use port 8090 in the URLs.

## Open `lift-station` first

[`lift-station`](lift-station/) is the flagship: a single project, one
`naut run`, that alone exercises all four languages, Modbus, alarms, retain,
online edits, and a full custom-component HMI. It's the "if you only open
one" example, and its README's "Tour the editor" section is the fastest way
to see what the VS Code extension does against a real project.

- **[`lift-station/`](lift-station/)** — a duplex sewage lift station: SFC
  duty/standby sequencing, an FBD level-to-speed PID, LD interlocks and a
  ladder-authored motor-starter library, Modbus TCP to the VFDs, a
  custom-component HMI mimic — and the same station in 3D beside it
  (`lift-station.scene.json`: flat-tag bindings, six kinds as glTF data),
  so the two views can be compared.
- **[`batch-skid/`](batch-skid/)** — a two-ingredient batch mixing skid: SFC
  recipe phases with every qualifier, an FBD dosing/temperature PID, an
  EtherNet/IP handshake with an "existing" Logix line (a read-only `.L5X`,
  diffable between revisions), and a built-in-components mimic.
- **[`remote-fleet/`](remote-fleet/)** — three standalone Sparkplug B edge
  sites (two wells, a booster station) and a central Sparkplug host SCADA
  project: store-and-forward across a broker outage, dark-site suppression,
  `drivers:` (two drivers on one scan), and a historian recipe.
- **[`go-sdk/`](go-sdk/)** — the one Go-tier example: a heated tank hosted by
  hand-written Go instead of a manifest, for the SDK story (a custom
  `io.Driver`, or plant physics too rich for `sim.st`). Everything else here
  is manifest-first, no Go.
- **[`spatial-rig/`](spatial-rig/)** — the office rig behind the **3D
  operator view**: three struct-tagged props with a simulated process, and a
  `rig.scene.json` rendered live by `@joyautomation/nautilus-hmi-3d` (orbit,
  click-to-inspect, alarm halos, quality greying). The test bed for the
  spatial HMI; the process is deliberately tiny.
- **[`well-site/`](well-site/)** — one production well, **drawn**: the
  same spatial package in its paper look (`look: paper`, an orthographic
  camera), an isometric line drawing where the ground is cut away to show
  the casing, the water in it and the submersible pump, the discharge
  run through a meter and a valve to a reservoir, and a hypochlorite feed
  — with a simulated aquifer behind it. The first screen in that style.
- **[`plclab/`](plclab/)** — a controller MHJ PLC-Lab drives directly
  over OPC UA, as it drives a CODESYS controller: the `PlcLabConnector`
  struct tag, anonymous writes for a lab, a byte-copy program in place of
  PlcLabSync, and a small tank in ladder. For teaching labs.

Each project runs the same way:

```sh
naut check .    # validate the bench build
naut test .     # the acceptance suite, virtual time
naut run .      # dashboard + tag API on http://localhost:8080
naut build .    # one deployable controller binary
```

(`lift-station` and `batch-skid` also have a second, field-facing manifest —
`naut check -m field.yaml .` and `naut check -m line.yaml .` — see each
project's own README under "Bench vs. field"/"Bench vs. line". `remote-fleet`
is four projects, not one — `naut run sites/well-1`, `sites/well-2`,
`sites/booster-1`, and `scada`, each its own terminal.)

## Pick by what you want to see

| Area | Project | File |
|---|---|---|
| ST: programs, UDTs (`TYPE`/`STRUCT`), a `VAR_IN_OUT` function block | lift-station | `lib/pump.st`, `lib/physics.st` |
| ST: a `FUNCTION_BLOCK` with a `VAR_OUTPUT` array pin, a `FUNCTION` | batch-skid | `lib/recipes.st` |
| FBD: a `PID` closed loop, a hysteresis seal-in | lift-station | `level.fbd` |
| FBD: totalizers, a second `PID` loop | batch-skid | `dosing.fbd` |
| Ladder: interlocks, branches, HOA | lift-station | `permissives.ld` |
| Ladder: a `FUNCTION_BLOCK` authored as rungs, instantiated twice | lift-station | `lib/motor.ld` |
| Ladder: a handshake permissive, a compare contact, a `TON`-gated alarm | batch-skid | `transfer.ld` |
| SFC: alternative + simultaneous divergence, a timer in an action body | lift-station | `sequence.sfc` |
| SFC: simultaneous divergence/convergence, Hold/Resume, Abort, every qualifier (`N`/`S`/`R`/`P1`/`P`/`P0`) | batch-skid | `phases.sfc` |
| Rockwell `.L5X`: a read-only ladder import, diffed between git revisions | batch-skid | `line/Line.L5X` |
| Multiple tasks/scan rates, a project `lib/` | lift-station, batch-skid | `nautilus.yaml` `tasks:`, `lib/` |
| Modbus TCP: device map, `naut modbus import`, a writable bit | lift-station | `devices.yaml`, `modbus_manifest.yaml` |
| Modbus TCP: a live device riding a Sparkplug edge node | remote-fleet | `sites/booster-1/devices.yaml` |
| EtherNet/IP: `naut eip import` against a "real" Logix controller | batch-skid | `line.yaml`, `eip_manifest.yaml` |
| Sparkplug B edge node: standalone sites, store-and-forward | remote-fleet | `sites/well-1`, `sites/well-2`, `sites/booster-1` |
| Sparkplug host: a fleet-wide SCADA, NCMD writeback, dark-site suppression | remote-fleet | `scada/` |
| `drivers:` — more than one driver on a scan | remote-fleet | `scada/nautilus.yaml` |
| Alarms: ISA-18.2 states, priorities, ack/shelve | lift-station, batch-skid | `alarms:` in each manifest |
| Alarms: one rule expanding across a shared Template + suppression | remote-fleet | `scada/nautilus.yaml` |
| Historian: a separate daemon polling into Postgres | remote-fleet | `compose.yaml`, `scada/README.md` |
| Retained state: setpoints/recipe select survive a restart | lift-station, batch-skid | `retain:` in each manifest |
| Redundancy: leader election, a two-replica deploy shape | remote-fleet | `scada/deploy/` |
| Online edits: Download/Rollback, diff vs controller, diff vs HEAD, diff between revisions | lift-station | README's "Tour the editor" |
| Acceptance tests: virtual time, `suspend:`, alarm `ack:`/`shelve:` | all four | `*_test.yaml` |
| HMI: a mimic with custom Svelte components and port overrides | lift-station | `hmi/`, `lift-station.mimic.json` |
| HMI: a mimic built entirely from the kit's built-in components | batch-skid, remote-fleet | `batch-skid.mimic.json`, `scada/fleet.mimic.json` |
| HMI: a 3D scene bound to struct tags (`@joyautomation/nautilus-hmi-3d`) | spatial-rig | `rig.scene.json`, `hmi/` |
| HMI: a 3D scene over flat tags (`bind`, no struct), its kinds as glTF data, beside the 2D mimic of the same station | lift-station | `lift-station.scene.json`, `hmi/models/build.py`, `hmi/src/routes/3d/` |
| OPC UA server: a simulator writing inputs anonymously, element node ids (`Inputs[17]`) | plclab | `nautilus.yaml` `opcua:`, `plclab.st` |
| The SDK path: a custom `io.Driver`, no manifest | go-sdk | `plant.go` |
| A plant simulation of IT hardware: a `replay` driver, fault inputs as tags, `naut snmp serve --from` | it-cluster | `lib/switch.st`, `replay_manifest.yaml` |

**Needs naut ≥ 0.13.1** (`lib/` composition, a `FUNCTION_BLOCK`'s declared
initial values, `naut compose`; 0.13.1 adds the dashboard fallback when a
project's HMI is not built yet), **except `remote-fleet`, which needs only
≥ 0.12.0**. Install or update with
`go install github.com/joyautomation/nautilus/cmd/naut@latest`, or take a
binary from the [releases page](https://github.com/joyautomation/nautilus/releases).

## In VS Code

Open any project's folder (or the whole repo) with the nautilus IEC 61131-3
extension installed: `.fbd`/`.ld`/`.sfc` files open as diagram editors
(*Open With → \<Diagram\>*), every diagram shows live values while
`naut run` is up, and each project's `*.mimic.json` opens in the mimic
editor the same way. See `lift-station/README.md`'s "Tour the editor" for a
guided ten minutes against a running project.

## Docs

- [Getting started](https://nautilus.joyautomation.com/) and the language
  references: [Structured Text](https://nautilus.joyautomation.com/languages/structured-text/),
  [Function Block](https://nautilus.joyautomation.com/languages/function-block/),
  [Ladder](https://nautilus.joyautomation.com/languages/ladder/),
  [SFC](https://nautilus.joyautomation.com/languages/sfc/)
- Guides: [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/),
  [EtherNet/IP](https://nautilus.joyautomation.com/guides/ethernet-ip/),
  [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/),
  [Sparkplug host](https://nautilus.joyautomation.com/guides/sparkplug-host/),
  [Alarms](https://nautilus.joyautomation.com/guides/alarms/),
  [Testing](https://nautilus.joyautomation.com/reference/testing/),
  [HMI kit](https://nautilus.joyautomation.com/reference/hmi/)
- [`docs/design/examples.md`](../docs/design/examples.md) — the brief this
  rebuild was built from, and [`docs/design/examples-dogfood.md`](../docs/design/examples-dogfood.md)
  — the friction log kept while building all four.
