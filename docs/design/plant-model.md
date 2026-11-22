# Design: the plant model

#714 (HMI kit: asset pages and screens from the plant model), part of the
parity epic #725. Builds on the asset page of slice 1 (#1232). James's call
(2026-11-22), option C: **`plant.yaml` owns equipment identity and topology;
mimics and 3D scenes own layout only and reference plant ids.** Introduced
additively: a drawing without `plant.yaml` works as before.

## 0. Why

"Equipment" was only a drawing object (a mimic's `equipment`, a scene's
node) or an alarm's grouping key. Asset pages, area pages and a process
scheme all need the same facts: what is in the plant, what kind it is, where
it is, what it feeds. Those facts had no home, and a second or third copy
(2D, 3D, argonaut's `parts`) would drift. One file holds them; a picture
refers to it.

## 1. The file

`plant.yaml`, named once in the manifest (`plant: plant.yaml`), never also
inline:

```yaml
areas:
  - { id: Receiving, label: "Receiving" }
equipment:
  - { id: P101,  type: Pump,   class: CentrifugalPump, area: Receiving, tag: P101,
      nozzles: [N1, N2] }
  - { id: TK101, type: Tank,   class: Tank, area: Receiving, tag: TK101, nozzles: [N1, N2] }
  - { id: CONV1, type: Conveyor, area: Receiving, device: CONV1 }
flows:
  - { from: TK101.N2, to: P101.N1, medium: water }
```

- **area**: `id`, `label`.
- **equipment**: `id` (an IEC-style identifier, unique), `type` (an asset
  type), `class` (optional; DEXPI-style, §4), `area`, `label`, and exactly one
  of `tag` (a declared tag, usually a UDT instance) or `device` (a `devices:`
  row). `nozzles` (optional) names the connection points.
- **flows**: `from`, `to` as `equipment` or `equipment.nozzle`, and an
  optional `medium`. Equipment to equipment, directed.

## 2. Asset types

`asset-types/<Type>.asset.yaml` beside `plant.yaml`: what a page of that
type shows. Each entry's `tag` is a template; `{tag}` is the equipment's
`tag`:

```yaml
type: Pump
class: CentrifugalPump
readings:
  - { tag: "{tag}.Running", label: State, on: Running, off: Stopped }
  - { tag: "{tag}.Flow", label: Flow, unit: gpm, decimals: 0 }
setpoints: [{ tag: "{tag}_SpeedSP", label: Speed, unit: "%" }]
commands:  [{ tag: "{tag}_Start", label: Start, kind: start }, { tag: "{tag}_Stop", label: Stop, kind: stop }]
toggles:   [{ tag: "{tag}_Auto", label: Auto }]
trend: ["{tag}.Flow"]
```

Equipment with a `device:` gets its page from the device's profile points
(as slice 1 does), titled and placed by the plant; an asset type is optional
for it.

## 3. How it relates to the drawings

- A mimic's `equipment` (and a scene's `node`) may carry `plant: <id>`. The
  drawing keeps its layout (`x`, `y`, `pos`, ports); the plant item supplies
  the identity (label today; tag and kind when the scheme generator of slice 3
  writes the drawing).
- `naut check` flags mismatches: a `plant:` that names no plant id is an
  error; a drawn piece with a `panel` and no `plant:` is a warning when a
  plant file exists; an alarm whose `equipment:` names no plant equipment is a
  warning.
- With no `plant.yaml` nothing changes, and no check mentions it.
- Generated at `naut hmi build`: `/assets/<id>` for each plant equipment
  (grouped by area on the index). A hand-written mimic panel or screen of the
  same slug wins.

## 4. DEXPI (not planned)

DEXPI (the ISO 15926-based P&ID exchange format) is not implemented: no
customer has brought DEXPI files. Where it costs nothing the model lines up
with it, so a future importer is a mapping, not a redesign:

- `class` uses DEXPI's equipment class names where an obvious match exists
  (`CentrifugalPump`, `Tank`, `PlateHeatExchanger`, `Valve` families), and
  `type` is the local asset type.
- Connections are equipment to equipment through a named nozzle, as DEXPI's
  `PipingNetworkSystem`/`PipingNetworkSegment` connect nozzles, with the
  `medium` as the segment's fluid code.
- An importer would map a DEXPI `Equipment` to a plant `equipment` (its
  `ComponentClass` to `class`, its `Nozzle`s to `nozzles`, its tag name to
  `id`), a piping segment between two nozzles to a `flow`, and leave `tag`
  or `device` for a person (or the P&ID's instrumentation) to bind: DEXPI says
  what the equipment is and how it connects, not which controller tag drives
  it. Instrumentation functions would map to readings, not to new equipment.

## 5. Plan

1. This brief, the context-map row and the dependency rule.
2. `internal/plant`: the format and its own rules (no manifest knowledge).
3. The manifest reads `plant:`; `naut check` rules; schema.
4. `naut hmi build` generates `/assets/<id>` from it.
5. Slice 3 (separate): area pages from the model, the process scheme as a
   mimic written once, `naut hmi scheme --update`, a drawing taking its tag,
   label and kind from `plant:`.

## 6. Built (slice 2)

`internal/plant` (format, rules), `plant:` in the manifest and the JSON
schema, `naut check` (the model's rules; each row's tag or device; each asset
type entry's expanded tag; alarm `equipment:` keys; a drawing's `plant:`),
`plant:` on a mimic's equipment and a scene's node, and the generated
`/assets/<id>` pages (`cmd/naut/assets.go`, `hmibuild.Options.Assets`). A
mimic equipment that names a `plant:` makes no page of its own; a device
that a plant equipment names makes no page of its own. Not yet: flows draw
nothing (slice 3), a drawing does not take its tag, label or kind from the
plant (slice 3), and the plant is not exported to the master alarm database.

Checked only to the root: a tag `Plant.Pump1` is checked as `Plant`, and an
asset type's `{tag}.Level` is checked as the tag's root, not against the
struct's members, so a misspelt member shows a blank reading (the member
check is a follow-up). Two ids whose page slug is the same (`P_1`, `P__1`),
or a plant id whose slug is a device's that no equipment names, are errors.
