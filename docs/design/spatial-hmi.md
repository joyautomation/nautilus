# Design brief: the spatial HMI package (`@joyautomation/nautilus-hmi-3d`)

Status: **Milestone 1 in progress** on the `spatial-hmi` branch (2026-09-26).
The R&D plan, device research and the office-rig spike this ports live in
`~/Development/joyautomation/randd/` (`spatial-hmi.md`,
`spatial-hmi-devices.md`, `spatial-rig/`); this brief is the repo's record of
what ships and why.

**Goal:** a live 3D process view as a supported layer of the Nautilus HMI,
not a demo: a package next to `hmi/` that renders a **scene document**
(`*.scene.json`, diffable, bound to struct/UDT tags) against the controller's
existing SSE stream and alarm API, in the same way `<Mimic>` renders a
`*.mimic.json`. The same document later drives AR placement (the R&D plan's
Phases 4b and 5), so 3D is the authoring and testing surface for AR.

## 0. Decisions already made (from the R&D plan; not relitigated here)

- **A separate package.** `hmi-3d/` publishes as
  `@joyautomation/nautilus-hmi-3d`. three.js and Threlte never enter
  `@joyautomation/nautilus-hmi`; the 2D kit's bundle stays what it is.
- **Scene as data.** One `*.scene.json` per view: nodes bind **struct tags**
  (`P101`) and read members; positions are **metres in the site frame**
  (origin marker at `[0,0,0]`, +y up), the same frame as the rig's
  `assets.yaml`, so a desktop scene position is an AR anchor position.
- **Served by the controller.** `server.hmi:` points at the built SvelteKit
  app (`adapter-static`, `ssr = false`), exactly like the 2D examples.
- **ISA-101 restraint for process state.** Equipment is grey when normal.
  Colour means abnormal (alarm priority) or a live state worth noticing
  (running, flowing). Realism is for the surroundings, later.
- **Read-only by default.** No write path in Milestone 1. When writes come
  they go through the kit's `writeTag` + `confirm()` and a per-scene
  `writable:` list (reserved in the schema now, rejected if non-empty until
  the write path exists).

## 1. Package layout & public API

```
hmi-3d/
  package.json            @joyautomation/nautilus-hmi-3d — svelte-package library, same
                          tooling as hmi/ (svelte-kit sync + svelte-package + publint)
  src/lib/
    index.ts              the public surface, listed below
    scene.ts              SceneDoc types, validateScene(), sceneTags()      (pure)
    bindings.ts           the binding grammar: resolveNodeBindings(), refRoot() (pure)
    alarms.ts             worstAlarmByAsset(): alarm instances → per-node worst  (pure)
    registry.ts           NodeKindDef, NodeRegistry, builtinRegistry
    palette.ts            colours read from the kit's theme tokens, with fallbacks
    perf.ts               fps + ts→pixel latency sampling                    (pure-ish)
    components/
      SceneView.svelte    the whole view: <Canvas>, Scene3D, HUD, inspector drawer
      Scene3D.svelte      renders a SceneDoc inside a Threlte <Canvas>
      AssetDrawer.svelte  click-to-inspect: the kit's 2D faceplate, members, quality, alarms
      PerfHud.svelte      the measurement HUD (fps, p95 ts→pixel), opt-in
      Tank3D.svelte  Pump3D.svelte  Valve3D.svelte   the built-in kinds
      Pipe3D.svelte  Halo.svelte  Label.svelte  Fixture3D.svelte
  tests/
    harness.ts            the kit's 60-line describe/it/expect subset, copied
    scene.test.ts  bindings.test.ts  alarms.test.ts
```

Public API (`index.ts`):

```ts
// Components
export { SceneView, Scene3D, AssetDrawer, PerfHud, Halo, Label, Pipe3D, Tank3D, Pump3D, Valve3D };
// Scene document
export type { SceneDoc, SceneNode, ScenePipe, SceneFixture, SceneCamera, Vec3 };
export { validateScene, sceneTags };                 // pure: validation + the ?tags= list
export type { SceneError };
// Bindings
export { resolveNodeBindings, refRoot, member, num, flowing };
// Registry
export type { NodeKindDef, NodeProps, NodeRegistry };
export { builtinRegistry };
// Alarms
export { worstAlarmByAsset };  export type { AssetAlarm };
// Measurement
export { PerfSampler };
```

Peer dependencies: `svelte ^5`, `three`, `@threlte/core`, `@threlte/extras`.
Dependency: `@joyautomation/nautilus-hmi` (the realtime/alarm clients, the
2D faceplates the drawer shows, `tagAt`, `PRIORITY_ORDER`, theme tokens).

A page that shows a scene is this, and nothing scene-specific:

```svelte
<script lang="ts">
  import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
  import { SceneView, sceneTags, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
  import rig from '../rig.scene.json';
  const doc = rig as SceneDoc;
  const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: sceneTags(doc) });
  const alarms = createAlarmClient(rt);
</script>
<SceneView {doc} {rt} {alarms} perf />
```

## 2. The scene document

```ts
type Vec3 = [number, number, number];

interface SceneDoc {
  name?: string;
  /** Initial camera. Positions in scene metres. */
  camera?: { pos: Vec3; target: Vec3; fov?: number };
  /** Static, unbound geometry: the desk, a floor slab, the origin marker. */
  fixtures?: SceneFixture[];
  /** A reference grid on a horizontal plane. */
  grid?: { pos?: Vec3; size?: [number, number]; cell?: number; section?: number };
  nodes: SceneNode[];
  pipes?: ScenePipe[];
  /** Reserved: tags an operator may write from this scene. Must be empty until the write path ships. */
  writable?: string[];
}

interface SceneNode {
  id: string;                         // unique in the doc; what a click reports
  kind: string;                       // registry key: 'tank' | 'pump' | 'valve' | yours
  tag?: string;                       // the struct tag the kind's component reads
  label?: string;                     // defaults to id
  pos: Vec3;                          // metres, site frame
  rot?: Vec3;                         // degrees, XYZ order; [0, yaw, 0] for a turn on the spot
  scale?: number;
  props?: Record<string, unknown>;    // static props passed to the component
  bind?: Record<string, string>;      // live props: prop -> ref (see §3)
}

interface ScenePipe {
  id?: string;
  points: Vec3[];                     // polyline, ≥ 2 points, metres
  radius?: number;                    // default 0.012
  bind?: { flowing?: string };        // same key the mimic's pipe binds
}

interface SceneFixture {
  kind: 'box' | 'plane' | 'marker';
  pos: Vec3; size?: Vec3 | [number, number]; rot?: Vec3;
  color?: string; opacity?: number;
}
```

`validateScene(doc, registry)` is pure and returns `{ ok, errors: SceneError[] }`
with a JSON-pointer-ish `path` on each error: unknown `kind`, duplicate `id`,
malformed `pos`/`rot`/`points`, a pipe with fewer than two points, a
non-empty `writable`, an empty or malformed binding ref. `SceneView` runs it
on load and renders the error list instead of a broken scene, the same
outcome `naut check` gives a bad manifest.

`sceneTags(doc)` returns the deduplicated list of **root** tags the document
reads (node `tag`s, the root of every `bind` ref, pipe `flowing` roots), which
is what the page hands `RealtimeClient({ tags })`. One filtered subscription
per scene; a scene of 40 assets subscribes to 40 struct tags.

**Where the file lives:** at the project root next to `nautilus.yaml` and
the `*.mimic.json`, so a future editor finds it and the app imports it with
one `../` (the same reason `lift-station.mimic.json` sits at the root).

## 3. The binding grammar

The mimic's grammar is: `bind: { prop: ref }`, a ref is a tag name, a
leading `!` negates a boolean, an absent tag leaves the prop unset. The
scene document uses **the same map and the same rules**, with one addition
that struct tags need: **a ref may be a dotted path** (`P101.Speed`), read
with the kit's `tagAt`. Rules, in order:

1. `!ref` negates: the prop is `value !== true`.
2. The ref is resolved as an absolute path from the frame's tag map
   (`Demand`, `P101.Speed`, `Line.Drives[0].Amps` is not supported; no
   indexing).
3. An absent path leaves the prop unset, so a component's default holds.

There is deliberately no "relative to the node's tag" shorthand. A kind's
**component** reads members off the node's whole struct (`value.Level`),
which is the relative form; `bind` exists for the explicit extras and
overrides, and an explicit ref that reads the same in a mimic, a scene and a
`curl /api/state` was worth more than saving `P101.` in the rare override.

The kit's `resolveBindings()` does not walk dotted paths today; the package
carries `resolveNodeBindings()` with the grammar above. Teaching the kit's
function dotted paths (additive, no behaviour change for plain names) would
make them one function, and is noted as a follow-up for the 2D kit rather
than done in this branch.

What a kind's component receives (`NodeProps`):

```ts
interface NodeProps {
  value: unknown;      // the node's struct tag, whole, or undefined
  good: boolean;       // quality of the node's tag AND every bound ref's root
  label: string;
  selected: boolean;
  [prop: string]: unknown;   // static `props` then resolved `bind`, bind wins
}
```

## 4. The node registry

```ts
interface NodeKindDef {
  component: Component<NodeProps>;
  /** Local-space box for the alarm halo and selection outline. */
  bounds: { size: Vec3; center: Vec3 };
  /** Where the floating label sits, local space. */
  labelAt: Vec3;
  /** The label's value text; pure so it is testable. */
  status?: (value: unknown, good: boolean) => string;
  /** The 2D faceplate the inspector shows; the drawer falls back to a member table. */
  faceplate?: Component<{ value: unknown; label: string }>;
}
type NodeRegistry = Record<string, NodeKindDef>;
export const builtinRegistry: NodeRegistry = { tank, pump, valve };
```

An app extends by spreading: `registry={{ ...builtinRegistry, server, switch, 'switch-port': port }}`.
Nothing in `Scene3D` names a kind; it looks every node up. That is how the
IT-hardware kinds from the drivers session (`it-drivers` branch,
`docs/design/it-drivers.md`) arrive without touching the core, and the
contract they need to meet is exactly `NodeKindDef` plus the UDT member
names their component reads.

The built-in kinds read the rig's UDTs (`examples/spatial-rig/types.st`):

| kind | reads | shows |
|---|---|---|
| `tank` | `Level` (%), `TempC` | fluid height inside a transparent shell |
| `pump` | `Running`, `Fault`, `Speed` (%) | coupling turns at 2 rev/s × speed; body green while running |
| `valve` | `Pos` (%), `Cmd` (%) | quarter-turn handle follows `Pos` |

## 5. Picking, alarms, quality, labels

- **Picking** uses Threlte's `interactivity()` plugin: each node's group
  gets `onclick`, and a click whose `delta` (pointer travel since
  pointerdown) exceeds 5 px is an orbit, not a pick. The spike had replaced
  the plugin with a hand-rolled raycast while chasing a stale bundle; the
  plugin is the deliberate choice now, and the raycast is gone. A pick sets
  `selected`, opens the inspector drawer, and calls `onselect(id)`.
- **Alarms**: `worstAlarmByAsset(instances)` folds the alarm client's
  instances to one `{ priority, unacked }` per asset, keyed by the alarm
  tag's root (`P101.Fault` → `P101`), skipping normal, shelved and
  suppressed. A node with an entry gets a `Halo`: a wireframe box on the
  kind's `bounds`, coloured by priority from the kit's tokens (`--crit`,
  `--serious`, `--warn`, `--accent`, `--muted`), pulsing while
  unacknowledged and steady once acked.
- **Quality**: a node is `good` only when its tag and every bound ref's root
  report good. A bad node renders desaturated and translucent, its label
  reads `stale` in italics, and a pipe bound to a bad tag greys out. A stale
  model must never look like a healthy one.
- **Labels** are `<HTML>` overlays at the kind's `labelAt`: title plus the
  kind's `status()` text. Declutter by distance is Milestone 2.
- **Colours** come from the theme: `palette.ts` reads the kit's tokens off
  the canvas element once at mount (with the dark-theme hex values as
  fallbacks), so the 3D view follows `[data-theme]` like every 2D component.

## 6. Performance budgets and measurement

Targets (from the R&D plan's Phase 1 exit):

| Measure | Desktop / laptop | Low-end panel PC |
|---|---|---|
| Frame rate, rig scene (3 assets, 2 pipes) | ≥ 60 fps | ≥ 30 fps |
| Tag change → pixel, p95 | ≤ 250 ms | ≤ 250 ms |
| JS bundle, gzipped | ≤ 600 kB (three + Threlte + kit) | same |
| Draw calls, a 40-asset scene | ≤ 300 | same |

Measurement is built in: `PerfSampler` counts `requestAnimationFrame` per
second and, per SSE frame, schedules two animation frames after the frame
arrived and records `Date.now() − frame.ts` (an upper bound: the frame after
the one that drew it). It ignores frames while `document.hidden`, because a
hidden tab throttles rAF to ~1 Hz and Threlte does not mount canvas
children until a frame is drawn, so a background tab's numbers are
nonsense. `PerfHud` shows fps, p95 and active alarms; `perf` on `SceneView`
turns it on. Browser and controller must share a clock (same machine, or
NTP) for the latency figure to mean anything.

**Results** are recorded in §9 as they are taken.

## 7. Example: `examples/spatial-rig/`

The office rig from `randd/spatial-rig/`, unchanged on the controller side:

```
nautilus.yaml    3 struct tags (T101 Tank, P101 Motor, XV101 Valve), setpoints,
                 fault-injection switches, 4 alarm rules, server.hmi: hmi/build
types.st         the Tank / Motor / Valve UDTs — the contract a scene binds to
control.st       pump seal-in on level, valve follows demand, limit bits
sim.st           the process, plus fault injection
rig_test.yaml    4 acceptance tests, virtual time (naut test)
assets.yaml      marker id → asset → tag, surveyed positions (AR, later)
rig.scene.json   the scene: 3 nodes, 2 pipes, fixtures, camera
hmi/             SvelteKit app: +page.svelte is the §1 snippet
```

`hmi/` depends on `@joyautomation/nautilus-hmi-3d` by `file:../../hmi-3d`
(the same way `tools/vscode-iec/webview-ui` depends on `hmi/`), so the
example exercises the package in this checkout, and on the kit from npm.
Because the package is a symlink, the example's Vite config dedupes
`svelte`, `three` and `@threlte/*` so exactly one copy of each runs (two
Svelte runtimes cannot share context; two three.js copies cannot share
`instanceof`). The example builds after `hmi-3d` has been packaged
(`npm run package` → `dist/`), which CI does in that order.

CI: the Go job already discovers `examples/spatial-rig/nautilus.yaml` and
runs `naut check` + `naut test` on it. A new `hmi-3d` job runs the package's
tests, type-check and package step, then builds the example app.

## 8. Milestone 2 and beyond: "like a game, but useful"

Proposed order, each with an exit and the capture moment it produces
(content ideas N-56, N-57, N-58 in `content/ideas.md`). Each is its own PR.

1. **glTF nodes and surroundings** (N-57's "realistic surroundings"). A
   `gltf` kind whose `props.src` is a model URL, with named-mesh hooks so a
   binding can drive a sub-mesh (`bind: { "spin:Impeller": "P101.Speed" }`
   is the shape to design; not decided). HDRI environment lighting and PBR
   materials on the built-ins; the ground becomes a textured plane. Exit:
   the rig scene with one CAD-derived pump model and an HDRI, at the §6
   budgets. Capture: before/after, grey spike vs lit scene.
2. **Overlays.** Labels declutter by distance and by importance (alarmed
   first); flow animation along pipes (a moving dash texture, direction
   from point order); a sparkline in space on hover. Exit: 40 labelled
   nodes readable at three zoom levels.
3. **Filters and view modes.** `alarm-only` (everything else at 15 %),
   `quality` (stale/bad only in colour), `thermal` (a bound member mapped
   to a colour ramp, `TempC` on the rig), `x-ray` (shells wireframe, contents
   visible), `maintenance` (run hours, faults). One `mode` prop on
   `SceneView`, a mode strip in the HUD. Exit: modes toggle without a
   reload and are recorded on video (N-57's beat).
4. **Cutaways.** A section box per node (`props.section`) and a global
   clipping plane on a slider, using three's `clippingPlanes` with capped
   materials where it matters (a tank's fluid). Exit: the rig tank cut open
   with its level visible; a cabinet with its door cut away once a glTF
   cabinet exists.
5. **Navigation.** `views:` in the document (named camera bookmarks per
   area), click-to-fly to a node, a walk mode for the AR rehearsal. Exit:
   a bookmark list in the HUD, fly-to on double-click.
6. **Gaussian-splat backdrop** (N-58). Research first (a side subagent
   compares the currently maintained three.js splat renderers and their
   Threlte fit), then a `splat` fixture kind with the scan placed by the
   surveyed origin. View-only, office only. Exit: the office scan behind
   the live rig, at ≥ 30 fps on the desktop.
7. **Quality tiers.** Post-processing (SSAO, bloom, outlines) and splats
   switch off below a GPU tier detected at start (`WEBGL_debug_renderer_info`
   plus a 1 s fps probe), with a manual override. Exit: the panel-PC
   budget holds with tiers on.

## 9. Measurements

To be filled in during Milestone 1 (visible browser window, this desktop:
AMD Radeon RX 7900 XT, Chrome).

| Date | Machine | Scene | fps | p95 ts→pixel | Bundle (gz) | Notes |
|---|---|---|---|---|---|---|

## 10. Risks & open questions

- **Publishing.** `publish.yml` publishes the 2D kit on a version bump on
  main. Whether `hmi-3d` joins that flow (a second job, a second
  `PUBLISH_*` variable, `version-sync` for it) is a release-model decision
  for the PR review; the package is versioned `0.1.0` and private until then.
- **HTTPS.** Not needed for the desktop 3D view; required for WebXR and
  camera access later (R&D plan, Phase 4b). Nothing here changes `server/`.
- **Two copies of three.js** is the classic failure of a symlinked
  package; the dedupe in §7 covers the example, and the README says so for
  anyone else linking the package locally.
- **Bundle weight.** three + Threlte + the kit is ~450 kB gzipped in the
  spike; glTF loaders, HDRI and post-processing will push it. Milestone 2
  splits those behind dynamic imports so the base view stays under budget.
- **`assets.yaml` vs the scene file** hold the same positions twice today
  (the scene's `pos` and the survey's `pos`). When AR lands, a node gains
  `marker:` and the survey moves into the scene file; until then the
  scene is authoritative for rendering and `assets.yaml` for the survey.
- **Editor.** Nothing in VS Code knows `*.scene.json` yet. A JSON schema
  in the extension (the `mimic` precedent) is the cheap first step; a 3D
  editor is not planned.
