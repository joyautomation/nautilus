# Design brief: the spatial HMI package (`@joyautomation/nautilus-hmi-3d`)

Status: **Milestone 1 built** on the `spatial-hmi` branch, PR #65 (2026-09-26);
the panel-PC measurement is the one exit criterion still open. **Milestone 2
item 1 (kinds as data, surroundings) in progress** on `spatial-kinds`, §3c.
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
    registry.ts           NodeKindDef, NodeRegistry, builtinRegistry, registryFor()
    drives.ts             data kinds: the drive vocabulary, evaluated from the doc  (pure)
    palette.ts            colours read from the kit's theme tokens, with fallbacks
    perf.ts               fps + ts→pixel latency sampling                    (pure-ish)
    components/
      SceneView.svelte    the whole view: <Canvas>, Scene3D, HUD, inspector drawer
      Scene3D.svelte      renders a SceneDoc inside a Threlte <Canvas>
      AssetDrawer.svelte  click-to-inspect: the kit's 2D faceplate, members, quality, alarms
      PerfHud.svelte      the measurement HUD (fps, p95 ts→pixel), opt-in
      Tank3D.svelte  Pump3D.svelte  Valve3D.svelte   the built-in kinds
      Pipe3D.svelte  Halo.svelte  Label.svelte  Fixture3D.svelte
      GltfNode.svelte     a data kind: a glTF whose named meshes the drives move  (dynamic import)
      Surroundings.svelte HDRI environment, shadows                                (dynamic import)
  models/
    build.py              Blender script that generates the built-ins' glTF
    tank.glb pump.glb valve.glb   the built-in kinds as data (ships in the package)
    kinds.json            their `kinds` block: models + drives, ready to paste
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
export { builtinRegistry, registryFor, BUILTIN_CONTRACT };
// Kinds as data (§3c): the drive vocabulary, evaluated from the document
export { DRIVE_CHANNELS, evalDrives, driveMembers, validateDrives, formatStatus, kindMembers };
export type { Drive, MeshState, SceneEnvironment, SceneTexture };
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
  /** Kind contracts and data kinds (§3b, §3c). */
  kinds?: Record<string, SceneKind>;
  /** The surroundings: an HDRI and shadows (§3c). Absent = the flat look. */
  environment?: SceneEnvironment;
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
  /** plane only: PBR maps, so the ground is concrete rather than a colour (§3c). */
  texture?: { map: string; normalMap?: string; roughnessMap?: string; repeat?: [number, number] };
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

## 3b. The contract, and the tooling that fills it in

The worry to design against: nobody should hand-type a scene. Tags already
have the answer in this repo — the UDT is the contract, and generators
(`naut sparkplug import`, `naut modbus import`, `naut tags import-csv`)
fill in the tag files while `naut check` catches what is wrong offline.
Scenes get the same shape, so a person, a script or an AI agent can build
one against a contract and get told, with a path, what does not fit.

**The contract is kind ↔ struct type.** A kind reads named members off a
UDT; that is what alarm rules match on and what a Sparkplug Template
publishes. It is spelled out in the scene file's `kinds` block:

```json
"kinds": {
  "pump":   { "type": "VfdPump" },
  "switch": { "type": "Switch", "members": ["PortsUp", "Fault"] }
}
```

- The built-in kinds carry defaults: `tank` → `Tank` (`Level`, `TempC`),
  `pump` → `Motor` (`Running`, `Fault`, `Speed`), `valve` → `Valve`
  (`Pos`, `Cmd`). A project whose UDT is named differently re-points a
  built-in kind with `type:` and keeps its members.
- A kind the app registers itself declares `type` and the `members` its
  component reads, so the checker can hold it to the same standard.
- A node needs `tag` and `kind`; a `bind` entry satisfies a member the
  tag lacks (the flat-tag case), which is the one escape hatch.

**`naut check` checks scene files** (`*.scene.json` at the project root)
the way it checks alarm rules, offline, against the composed project:

| Finding | Severity | Why |
|---|---|---|
| node `kind` unknown (not built-in, not in `kinds`) | error | it would never render |
| node `tag` declared, but its type is not the kind's `type` and the missing members are not bound | error | the type is right there; nothing at run time makes `.Level` appear |
| a `bind` / pipe ref names a member the struct does not have | error | same |
| `!ref` on a member that is not a BOOL | warning | almost always a mistake |
| a root tag the manifest does not declare | warning | on a Sparkplug host, tags arrive from the field |
| duplicate id, malformed position, pipe with one point, non-empty `writable` | error | structural |

**`naut scene init` generates a starter scene.** It walks the composed
project's struct tags, gives every tag whose type maps to a kind one node
(built-in defaults plus `--kind Type=kind` for the rest), lays them out on
a grid labelled by tag name, fits the camera, and writes `kinds` for
any remaps. It refuses to overwrite without `--force`, and names the
struct tags it could not place so the next run can. The person then moves
things; nobody types a node.

**A JSON Schema** for `*.scene.json` ships in the VS Code extension (the
mimic precedent), so autocomplete and red squiggles exist before any
custom tooling does. The schema plus `naut check` are also the loop an AI
agent works in: generate, check, fix the paths it is told about.

**Then, in Milestone 2 (§8):** placement by dragging, written back to the
file (item 2), and kinds defined as data (a glTF and a few bindings, no
Svelte) so a new kind is a file and ten lines of JSON (item 1). Those two
remove the last hand-authoring: placement and geometry.

## 3c. Kinds as data: a glTF and a few drives

A kind was a Svelte component (§4). That is the right escape hatch for
genuinely new behaviour and the wrong front door: a new pump should be a
model file and ten lines of JSON, authored by the same person who places
the node, checked by the same `naut check`. So a kind may instead be
**data**, declared in the scene file's `kinds` block (§3b) alongside its
UDT contract:

```json
"kinds": {
  "pump": {
    "type": "Motor", "members": ["Running", "Fault", "Speed"],
    "model": "models/pump.glb",
    "bounds": "auto",
    "status": "{Speed:0} %",
    "drive": [
      { "mesh": "Coupling", "spin": { "axis": "x", "revPerS": { "bind": "Speed", "scale": 0.02 } } },
      { "mesh": "Motor",    "tint": { "bind": "Running", "on": "running" } },
      { "mesh": "Volute",   "tint": { "bind": "Running", "on": "running" } }
    ]
  },
  "tank": {
    "model": "models/tank.glb",
    "drive": [{ "mesh": "Fluid", "scale": { "axis": "y", "to": { "bind": "Level", "scale": 0.01, "min": 0.01 } } }]
  }
}
```

**The shape.** A data kind is `SceneKind` (§3b) plus:

| field | meaning |
|---|---|
| `model` | a glTF/GLB, as a URL path from the app root (`models/pump.glb` → `/models/pump.glb`). Units are metres, +y up; the node's `pos`/`rot`/`scale` place the model's origin. |
| `bounds` | `"auto"` (default: the loaded model's box) or `{ size, center }` — the halo and selection box. |
| `labelAt` | where the label floats; default the top centre of the bounds. |
| `status` | the label's value text as a template over members: `{Level:1} %` → `48.3 %`; `{Running?run:stopped}`. `stale` when quality is bad. |
| `drive` | the list of drives below. |

A data kind that names a **built-in** (`pump` with a `model`) keeps the
built-in's contract, status text and faceplate and replaces only the
geometry; a data kind under a new name gets the drawer's member table.
When a kind has no `model`, or its model fails to load, the built-in
Svelte kind of that name renders — so a project with no assets still
renders, and a bad path is a console warning and a grey pump, not a hole.

**Drives** are the small, fixed vocabulary of things a live value can do to
a **named mesh** in the model, evaluated from the document the way bindings
are (§3). Each drive names one mesh and one channel:

| channel | shape | does |
|---|---|---|
| `spin` | `{ axis, revPerS: Num }` | turns the mesh continuously about its local axis |
| `turn` | `{ axis, deg: Num }` | sets the mesh's rotation about its local axis |
| `scale` | `{ axis: x\|y\|z\|xyz, to: Num }` | sets the mesh's scale on that axis (a fluid whose origin is its base) |
| `tint` | `{ bind, on, off? }` | the mesh's colour is `on` while the value is true, else `off` or the model's own |
| `emissive` | `{ bind, on, intensity? }` | the mesh glows `on` while true |
| `visible` | `{ bind }` | the mesh is shown while true |

`Num` is `{ bind, scale?, offset?, min?, max? }`: the member, times `scale`,
plus `offset`, clamped. A boolean channel's `bind` is `true`, or a number
above `threshold` (default 0); a leading `!` negates, as everywhere else. A
`bind` names a **member of the node's struct** (the relative form a
component reads, §3), and the node's own `bind` map overrides it by the
same rule the built-ins use (`level` overrides `Level`). Colours (`on`,
`off`) are palette slots (`running`, `fluid`, `handle`, `stale`, a
priority name) or CSS colours; ISA-101 restraint is kept by using the
palette: equipment is its own grey until a state worth noticing.

**The members a kind reads** are `members` plus whatever its drives and
status template name, so a drive never repeats the list; `naut check`
holds every node to that union (§3b), and reports a drive whose `mesh`
the model does not contain when the model is loaded (in the browser, a
warning; offline the file is only checked to exist). Quality is
whole-model: a bad node renders every mesh in the stale grey, translucent,
with its drives frozen — the same rule as the built-ins.

**What is not in the vocabulary, on purpose:** arithmetic between members,
colour ramps (item 4's `thermal` mode is a view mode, not a drive), and
animation clips. A kind that needs those is a Svelte component in the
registry (§4), which is unchanged.

**Surroundings** are document data too. `environment` sets an HDRI for
image-based lighting and, optionally, the backdrop and shadows:

```json
"environment": {
  "hdri": "env/industrial_workshop_foundry_1k.hdr",
  "background": "none | sky | ground",
  "floor": -0.75,
  "intensity": 1,
  "shadows": true
}
```

`intensity` scales the HDRI's light on the models (not the backdrop); a
bright workshop HDRI at 1 swamps any key light, so a scene that wants
visible shadows turns it down (the rig uses 0.45) and the key light is
sized to show against it. `background: "ground"` projects the HDRI onto a floor at `floor` metres
(three's `GroundedSkybox`), so a desk-scale rig stands in the workshop
rather than floating in it; `sky` is the plain equirect backdrop; `none`
(default) lights and reflects only. `shadows` turns on a shadow-casting
key light with soft (PCSS) shadows on the desktop tier; item 8 makes that
tier a detected one. A `plane` fixture takes `texture` (PBR maps, tiled by
`repeat`), which is how the floor becomes concrete. The built-in Svelte
kinds get PBR materials (metalness/roughness) with their colours unchanged,
so the same greys read as painted steel under the HDRI.

**Look.** `SceneView` takes `look: 'lit' | 'flat'` (default `lit`). `flat`
renders the document as Milestone 1 did — built-in primitives, the three
lights, no environment, no textures — which is the fallback for a project
with no assets, the low tier until item 8, and the "before" half of the
N-57 cut, switched live from the HUD.

**Assets and paths.** Model, HDRI and texture paths are URL paths from the
app root, which is `hmi/static/` in source and the built app's root when
the controller serves it (`server.hmi`). `naut check` resolves each path
next to the scene file, then under the HMI app's `static/`, then in the
build output, and names all three when it finds nothing. Loaders are
**dynamic imports** (`GltfNode`, `Surroundings`), so the base bundle stays
at the Milestone 1 size and a scene with no models or environment never
fetches them (§6). The package ships the built-ins' models
(`hmi-3d/models/`, generated by a Blender script kept next to them) and
the `kinds.json` that declares them; an app copies the models it uses into
its `static/`.

**In three places, one contract.** `internal/scene` (Go) validates the
block offline with the same rules — a channel from the vocabulary, one per
drive, a `mesh` name, a `bind` member, files that exist — `hmi-3d`'s
`validateScene` does it in the browser and in tests, and the extension's
schema does it as you type. A sync test on each side reads the schema's
channel enum and the built-in kind names so the three cannot drift apart
silently.

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
`registryFor(doc, registry)` then lays the document's data kinds (§3c) on
top: a kind with a `model` becomes a `GltfNode` entry whose bounds are
`'auto'` until the model reports them, and it inherits `status`/`panel`
from the registry entry of the same name if there is one.
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
arrived and records two latencies. **rx→pixel** is arrival in the browser
to the frame after the one that drew it, on the browser's own clock, so it
is honest from any device; it is the render cost and the primary figure.
**ctrl→pixel** is `Date.now() − frame.ts`, the controller's stamp to the
pixel, which adds the network hop but only means anything when the two
clocks agree (same machine, or NTP); the HUD shows it only while it is
plausible and otherwise reports the clock offset instead. Both ignore
frames while `document.hidden`, because a hidden tab throttles rAF to
~1 Hz and Threlte does not mount canvas children until a frame is drawn.
`PerfHud` shows fps, both p95s and active alarms; `perf` on `SceneView`
turns it on.

The base bundle carries neither the glTF loader nor the HDRI loaders: both
live behind dynamic imports that only a document with a `model` or an
`environment` triggers, so the §6 JS figure is measured twice, base and
with the item 1 chunks loaded (§9).

**Results** are recorded in §9 as they are taken. The desktop passes with
room to spare: 60 fps is the display's refresh, and 33 ms is one SSE frame
plus two animation frames. The low-end panel PC run is still to do; it needs
the hardware, and until then the ≥ 30 fps target is unverified.

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
   *Designed as §3c (the `kinds` block grows `model`/`drive`, not a
   `gltf` kind with `spin:Impeller` bind keys: a kind's geometry and its
   drives belong to the kind, and the node keeps placing and binding);
   built on `spatial-kinds`, PR #66, 2026-09-27: the rig renders its
   three kinds from `models/*.glb` with drives, an HDRI ground-projected
   onto a concrete floor, soft shadows, at 60 fps / 33 ms (§9); the take
   is `content/assets/capture/n57/out/01-grey-to-lit.mp4`.*
2. **Place equipment by dragging.** Locating things is the one authoring
   step `naut scene init` cannot finish, and a number typed into `pos` is
   the tedium to remove. An edit mode on `SceneView`: click an asset,
   drag it on the ground plane (Threlte's `TransformControls`, snapped to
   the grid cell, Shift for free), rotate with a handle, and the document
   updates in memory with the new `pos`/`rot`. Where the numbers go:
   - **In VS Code**, a `*.scene.json` custom editor on the mimic editor's
     precedent, hosting this same package in a webview; a drag edits the
     document text, undo/redo and diff come for free, and the live values
     come from the running controller as the mimic's do. This is the real
     answer, and the reason the renderer is a package.
   - **In the browser**, `npm run dev` gets a Vite plugin endpoint that
     writes the file back, and a built app offers "copy scene JSON" /
     download, so a phone on the rig can still place a prop.
   Exit: the rig's three nodes placed by dragging, the diff showing only
   `pos` lines. Capture: the drag, then `git diff`.
3. **Overlays.** Labels declutter by distance and by importance (alarmed
   first); flow animation along pipes (a moving dash texture, direction
   from point order); a sparkline in space on hover. Exit: 40 labelled
   nodes readable at three zoom levels.
4. **Filters and view modes.** `alarm-only` (everything else at 15 %),
   `quality` (stale/bad only in colour), `thermal` (a bound member mapped
   to a colour ramp, `TempC` on the rig), `x-ray` (shells wireframe, contents
   visible), `maintenance` (run hours, faults). One `mode` prop on
   `SceneView`, a mode strip in the HUD. Exit: modes toggle without a
   reload and are recorded on video (N-57's beat).
5. **Cutaways.** A section box per node (`props.section`) and a global
   clipping plane on a slider, using three's `clippingPlanes` with capped
   materials where it matters (a tank's fluid). Exit: the rig tank cut open
   with its level visible; a cabinet with its door cut away once a glTF
   cabinet exists.
6. **Navigation.** `views:` in the document (named camera bookmarks per
   area), click-to-fly to a node, a walk mode for the AR rehearsal. Exit:
   a bookmark list in the HUD, fly-to on double-click.
7. **Gaussian-splat backdrop** (N-58). Research first (a side subagent
   compares the currently maintained three.js splat renderers and their
   Threlte fit), then a `splat` fixture kind with the scan placed by the
   surveyed origin. View-only, office only. Exit: the office scan behind
   the live rig, at ≥ 30 fps on the desktop.
8. **Quality tiers.** Post-processing (SSAO, bloom, outlines) and splats
   switch off below a GPU tier detected at start (`WEBGL_debug_renderer_info`
   plus a 1 s fps probe), with a manual override. Exit: the panel-PC
   budget holds with tiers on.

## 9. Measurements

To be filled in during Milestone 1 (visible browser window, this desktop:
AMD Radeon RX 7900 XT, Chrome).

| Date | Machine | Scene | fps | p95 ts→pixel | Bundle (gz) | Notes |
|---|---|---|---|---|---|---|
| 2026-09-26 | mira1 desktop, Radeon RX 7900 XT, Chrome (headed, 2560×1440) | rig: 3 nodes, 2 pipes, desk, grid | 60 (vsync) | ctrl→pixel 33 ms (n=197); 34 ms (n=200) on a second take | 438 kB | controller on the same machine (shared clock); 0 console errors; recipe `content/assets/capture/n56/browser/01-first-render.mjs` |
| 2026-09-27 | same desktop, Chrome headed 2560×1440 | rig, `look: flat` (M1 primitives, three lights) | 60 | rx→pixel 33 ms, ctrl→pixel 34 ms (n=50) | 254 kB base | item 1 build; `naut run` serving `hmi/build`; recipe `content/assets/capture/n57/browser/01-grey-to-lit.mjs`, first half |
| 2026-09-27 | same | rig, `look: lit`: 3 glTF kinds (pump 96 kB, tank 74 kB, valve 47 kB), HDRI 1k ground-projected (1.7 MB), concrete floor (3 × 1k JPG, 1.3 MB), soft shadows | 60 | rx→pixel 33 ms, ctrl→pixel 34 ms (n=191), unchanged through the cut and a fault | 254 kB base + 48 kB on demand (glTF chunk 14 kB, HDRI + shadows chunk 34 kB) = 302 kB | same take, second half; 0 console errors; the draco/basis decoders Vite emits (1.9 MB) are never fetched — no model uses them |

Bundle figures from 2026-09-27 on are the sum of gzipped JS under the
built app's `_app/immutable` (excluding the on-demand decoder assets),
which is what a browser downloads for a first paint; the 438 kB Milestone
1 figure was taken from Vite's build report and is not directly
comparable — measured this way, the Milestone 1 base and the item 1 base
are the same 254 kB, because the loaders sit behind dynamic imports.

## 10. Risks & open questions

- **Publishing — decided 2026-09-26: wait until it is baked.** `hmi-3d`
  stays in-repo (consumed by `file:` link) through Milestone 2's first
  item, so the first npm version already carries kinds-as-data. Joining
  `publish.yml`'s publish-on-bump flow then means a second job and
  `PUBLISH_*` variable, a `version-sync` line, a one-time manual first
  publish to create the package for npm's trusted publishing, and the
  example moving from `file:` to a version range.
- **HTTPS.** Not needed for the desktop 3D view; required for WebXR and
  camera access later (R&D plan, Phase 4b). Nothing here changes `server/`.
- **Two copies of three.js** is the classic failure of a symlinked
  package; the dedupe in §7 covers the example, and the README says so for
  anyone else linking the package locally.
- **Bundle weight.** three + Threlte + the kit is ~450 kB gzipped in the
  spike; glTF loaders, HDRI and post-processing will push it. Item 1 put
  the glTF and HDRI loaders behind dynamic imports (§3c) so the base view
  stays where it was; post-processing (item 8) gets the same treatment.
- **Asset weight in git.** A 1k HDRI is ~1.7 MB and a 1k PBR texture set
  ~1.3 MB; the example commits one of each, CC0 from Poly Haven, and no
  more. Larger sets belong in a release asset or the project's own store.
- **`assets.yaml` vs the scene file** hold the same positions twice today
  (the scene's `pos` and the survey's `pos`). When AR lands, a node gains
  `marker:` and the survey moves into the scene file; until then the
  scene is authoritative for rendering and `assets.yaml` for the survey.
- **Editor.** Nothing in VS Code knows `*.scene.json` yet. A JSON schema
  in the extension (the `mimic` precedent) is the cheap first step; a 3D
  editor is not planned.
