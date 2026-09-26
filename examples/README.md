# examples — four plants, not four feature demos

Four small, believable control projects that between them cover everything
nautilus supports: every IEC 61131-3 language, every field driver, alarms,
historian, retain, redundancy, online edits, and an HMI mimic. None of them
is a client's plant — generic water/wastewater, batching, and remote-site
patterns, deliberately small so they run on a laptop with no hardware.

## Open `lift-station` first

[`lift-station`](lift-station/) is the flagship: a single project, one
`naut run`, that alone exercises all four languages, Modbus, alarms, retain,
online edits, and a full custom-component HMI. It's the "if you only open
one" example, and its README's "Tour the editor" section is the fastest way
to see what the VS Code extension does against a real project.

- **[`lift-station/`](lift-station/)** — a duplex sewage lift station: SFC
  duty/standby sequencing, an FBD level-to-speed PID, LD interlocks and a
  ladder-authored motor-starter library, Modbus TCP to the VFDs, and a
  custom-component HMI mimic.
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
| The SDK path: a custom `io.Driver`, no manifest | go-sdk | `plant.go` |

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
