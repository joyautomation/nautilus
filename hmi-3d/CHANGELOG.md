# Changelog — @joyautomation/nautilus-hmi-3d

## 0.1.0 — unreleased

The first cut: the office-rig spike from `randd/spatial-rig` ported into
the repo as a package. `SceneView` renders a `*.scene.json` (nodes bound
to struct tags, pipes, fixtures, a grid, a camera) with the built-in
`tank`, `pump` and `valve` kinds; a registry the app extends; bindings in
the mimic's grammar plus dotted paths; click-to-inspect with the kit's 2D
faceplates in a drawer; alarm halos by priority; quality greying; theme
colours from the kit's tokens; and a `perf` HUD for fps and tag-change →
pixel latency. The scene file's `kinds` block is the kind ↔ UDT contract
`naut check` and `naut scene init` work from. Brief: `docs/design/spatial-hmi.md`.
