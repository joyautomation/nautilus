# Changelog — @joyautomation/nautilus-hmi-3d

## 0.1.2 — 2026-10-05

**Fixed.** The rack's frame: the top beam was drawn inside the unit space,
over the top unit's holes, and the bottom beam on the floor below where the
units start. The beams now sit just below the first unit and just above the
last, the posts stand below the bottom beam as feet, and the mounting rails
span exactly the units. Devices, holes and cables do not move. (#226)

## 0.1.1 — 2026-10-04

**Fixed.** The SYS-112B-FWT profile's ports face the front: the chassis has
every port on its front face, but they were drawn facing the rear, so cables
left through the back of the rack. (#159)

## 0.1.0 — 2026-10-04

The first release. Design: `docs/design/spatial-hmi.md`.

**Scenes.** `SceneView` renders a `*.scene.json` (nodes bound to struct
tags, pipes, fixtures, a grid, a camera) with the built-in `tank`, `pump`
and `valve` kinds and a registry the app extends. Bindings use the mimic's
grammar plus dotted paths. It has click-to-inspect with the kit's 2D
faceplates in a drawer, alarm halos by priority, quality greying, theme
colours from the kit's tokens, and a `perf` HUD for fps and tag-change →
pixel latency. The scene file's `kinds` block is the kind ↔ UDT contract
that `naut check` and `naut scene init` work from.

**Kinds as data.** A `kinds` entry with a `model` is a glTF kind: named
meshes driven from the node's struct by a fixed vocabulary (`spin`, `turn`,
`scale`, `tint`, `emissive`, `visible`), a `status` template and
`bounds: "auto"`. The built-ins ship as models (`models/`, from a Blender
script) and as `models/kinds.json`. Without a model, or when one fails to
load, the Svelte kind of that name renders. An `environment` block (HDRI, a
sharper `backdrop`, `sky`/`ground` projection, `intensity`, `fog`,
`shadows`) and PBR maps on a `plane` fixture make the surroundings real,
while the process state keeps its greys. `SceneView look="flat"` is the
asset-free look. The glTF and HDRI loaders are dynamic imports.

**Components define, documents place.** `<Node>` and `<Pipe>` let an app
build kinds as Svelte components, with component kinds and assemblies.

**Hardware (`./hardware`).** Servers and switches drawn from chassis
profiles (`./profiles/*`: Supermicro SYS-112B-WR and SYS-112B-FWT, FS
S3900-24T4S-R), with `validateProfile` and `resolveParts`. The rest:
- **The rack:** EIA-310 rails and ears, and slide rails.
- **Cables** between exact ports, checked against a declared topology:
  confirmed / consistent / contradicted / down / unverified, with reasons.
- **The mesh view.**
- **VLANs,** declared and checked per link, shown as a floor per VLAN with
  islands where a trunk is pruned.
- **Overlays:** heat, interfaces, free, identify, cables, VLANs and traffic
  (load in bands of line rate, drops as the evidence of saturation).
- **Alarm signs** on the device and the part.
- **The focus fade.**

The view of a device that is offline (`{node}__Online` false) shows none of
its last-held values as live.
