# Changelog — @joyautomation/nautilus-hmi-3d

## 0.2.0 — 2026-10-16

**Added.** The paper look (`look: "paper"` in the document, or
`<SceneView look="paper">`; design §3f): the scene as an isometric line
drawing — near-white fills, every edge inked, a paper backdrop — where
the only colour is an alarm, drawn as a soft glow
instead of the wireframe cage, and flow is the chevrons moving. The lines
are `PaperOutlines`, an edge pass over every mesh in the scene that a kind
opts a mesh out of with `userData={{ ink: false }}`; the colours are
`paperPalette(theme)`, laid over the theme's so the priority colours come
through untouched. `SceneDoc.look` is the document's choice, and the
`look` prop now overrides it rather than defaulting to `lit`.

**Added.** `camera.projection: "orthographic"` with `camera.width` (the
metres across the view): no vanishing point, so a position on a 45°
azimuth and a 35.26° elevation is the isometric drawing. The perspective
camera and its `fov` are unchanged and remain the default.

**Changed.** Labels are lettering, not cards, in every look: the title
and the status as text with a tight halo in the backdrop's colour (the
paper, or the theme's surface), no box and no border, so the drawing
shows between the letters. They are sized in metres of the scene and
clamped to 10–14 px, so on a phone they shrink with the drawing instead
of covering it, and in perspective the far labels are the small ones.

**Added.** Pipes carry chevrons along the run while it flows, in the order
of the run's points, so direction reads even where colour cannot; a pipe
sets `arrows: false` to go without. In the lit and flat looks they ride
the pipe in the shell grey; on paper they are the ink.

**Changed.** The flat look's pipes and bends have more facets in the paper
look so the edge pass draws a pipe as its silhouette.

`naut check` and the extension's schema know `look`, `camera.projection`,
`camera.width` and `pipes[].arrows`.

**Changed.** A data kind (a `kinds` entry with a `model`) now draws in
every look. The flat and paper looks left model kinds to the Svelte kind
of the same name, so a scene whose kinds are all glTF under their own
names — `examples/lift-station` — was labels over nothing once the look
button was pressed. `GltfNode` now reads the view: flat wears the model's
colours with no metalness and no maps (the built-ins' values), paper a
near-white fill (the palette's fluid grey where the model is translucent)
for `PaperOutlines` to ink, and a drive's tint or emissive goes on top of
either. A model kind under a built-in's name still yields to the built-in
in those looks, which is the before/after the flat look exists for;
`registryFor` takes the look as a fourth argument to say so.

**Fixed.** A data kind's translucent meshes (a well's water, a sight
glass) took the click: the pick stops at the nearest hit, so every click
on a pump standing in the well selected the well. A mesh whose material
is transparent with opacity below 1 now takes no clicks — the eye looks
through it, and so does the pick. The selection cage took the click too:
once a well was selected, its box surrounded the pumps in it and every
click on a pump re-selected the well. It no longer raycasts.

**Added.** Escape deselects: the selection cage goes and the drawer
closes; a bound `selected` reads `null`.

**Fixed.** The drawer of a flat-tag node (a `bind` map, no `tag`) showed
its quality and nothing else. It now lists every bound ref under its
member name, with the tag it reads, and its quality is the worst of
those tags'.

**Fixed.** A node's label read its status off the struct tag alone. A
flat-tag project (`"bind": { "level": "LIT101_Level" }` on a `tank` with
no `tag`, the arrangement the README describes) drew the fluid at the
bound level and labelled it `0.0 %`; a data kind's `{Level:1} %` read `—`.
A kind's `status` — the built-ins', a data kind's template, a component's
`kind` export — now receives the node's resolved `bind` map as a third
argument, the same overrides the geometry reads, so the label and the
model agree. The first flat-tag scene is `examples/lift-station`.

## 0.1.4 — 2026-10-15

**Fixed.** The VLAN view held one ERPS state for the whole site: a failed
link in one ring opened every ring's protection link. Each ring's RPL is
now blocked or open by its own ring alone (`ringsOf`): the ring links joined
through shared switches, or the new `TopoLink.ring` name where two rings
share a switch. `TopoLink.rpl` is now typed. (#153)

**Fixed.** A fan or PSU whose RPM or OutputW is not published read `0 rpm`
or `0 W`; it now reads `no data`. A reported 0 still reads 0. (#153)

**Fixed.** `PartModel` kept the previous model's materials and named meshes
when `src` changed (LEDs, accents and ports of the old model were still
driven, and its materials never freed), and rebuilt every material's shader
on each prop change; it now releases them on a new model and sets
`needsUpdate` only when the blending changes. `GltfNode` cloned a mesh's
material from what it wore at the time, which under bad quality was the
stale grey, so the part stayed grey once good again; it now clones the
model's own material, and releases its clones when the model changes. (#153)

## 0.1.3 — 2026-10-05

**Added.** `TopoLink.expect: 'up' | 'down'`. A link declared down (a
service port left open for a visitor's laptop) is consistent while it has
no link ("down, as declared") and contradicted the moment an end has link
("something is plugged in"); unreported stays unverified. The VLAN view
counts a declared-down link as carrying only while it is contradicted, so
neither its far end nor a ring's protection link is misread. (#228)

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
