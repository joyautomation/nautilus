# Changelog — @joyautomation/nautilus-hmi-3d

## 0.1.0 — unreleased

**Kinds as data (Milestone 2, item 1).** A `kinds` entry with a `model`
is a glTF kind: named meshes driven from the node's struct by a fixed
vocabulary (`spin`, `turn`, `scale`, `tint`, `emissive`, `visible`), a
`status` template, `bounds: "auto"`. The built-ins ship as models
(`models/`, from a Blender script) and as `models/kinds.json`; without a
model, or when one fails to load, the Svelte kind of that name renders.
An `environment` block (HDRI, `sky`/`ground` backdrop, `intensity`,
`shadows`) and PBR maps on a `plane` fixture make the surroundings real
while the process state keeps its greys; the built-ins get PBR values in
the lit look. `SceneView look="flat"` is the asset-free Milestone 1 look.
The glTF and HDRI loaders are dynamic imports. `registryFor`, `drives.ts`,
`kindMembers`, `BUILTIN_CONTRACT`; a contract test against the extension
schema and `kinds.json`. Design: `docs/design/spatial-hmi.md` §3c.


The first cut: the office-rig spike from `randd/spatial-rig` ported into
the repo as a package. `SceneView` renders a `*.scene.json` (nodes bound
to struct tags, pipes, fixtures, a grid, a camera) with the built-in
`tank`, `pump` and `valve` kinds; a registry the app extends; bindings in
the mimic's grammar plus dotted paths; click-to-inspect with the kit's 2D
faceplates in a drawer; alarm halos by priority; quality greying; theme
colours from the kit's tokens; and a `perf` HUD for fps and tag-change →
pixel latency. The scene file's `kinds` block is the kind ↔ UDT contract
`naut check` and `naut scene init` work from. Brief: `docs/design/spatial-hmi.md`.
