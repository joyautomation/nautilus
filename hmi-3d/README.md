# @joyautomation/nautilus-hmi-3d

The **spatial layer of the [nautilus](https://github.com/joyautomation/nautilus)
HMI kit**: a live 3D process view rendered from a `*.scene.json` document
bound to struct tags, on [Threlte](https://threlte.xyz) (Svelte 5 +
three.js). It is to `@joyautomation/nautilus-hmi`'s `<Mimic>` what a plant
walk-down is to a P&ID: the same tags, the same alarms, in space.

A scene is **data**: nodes placed in metres, pipes run between them, each
node bound to one struct tag (`P101`) whose members drive the model. One
file, diffable and reviewable next to the program it belongs to, rendered
by one component. The same file will later place AR overlays, so the
desktop view is the authoring and testing surface for that.

three.js never enters the 2D kit; this package is the one that carries it.
Brief and design: [`docs/design/spatial-hmi.md`](../docs/design/spatial-hmi.md).

## Install

```sh
npm install @joyautomation/nautilus-hmi-3d @joyautomation/nautilus-hmi svelte three @threlte/core @threlte/extras
```

Svelte 5, three and Threlte are peer dependencies. A SvelteKit host
(`adapter-static`, `ssr = false`: WebGL is a browser thing) is assumed.

## Usage

```svelte
<script lang="ts">
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, sceneTags, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import rig from '../rig.scene.json';

	const doc = rig as SceneDoc;
	// One SSE subscription, filtered to exactly the tags the scene reads.
	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: sceneTags(doc) });
	const alarms = createAlarmClient(rt);
	onMount(() => {
		rt.start();
		alarms.start();
		return () => {
			alarms.stop();
			rt.stop();
		};
	});
</script>

<SceneView {doc} {rt} {alarms} />
```

`SceneView` validates the document (and shows the errors, with paths,
instead of a blank canvas), renders it, orbits with the mouse, opens the
kit's 2D faceplate for a clicked asset in a drawer, halos assets with active
alarms by priority, greys anything whose tag is not good quality, and with
`perf` shows fps and tag-change → pixel latency.

## The scene document

```json
{
	"name": "Office rig",
	"camera": { "pos": [2.2, 1.6, 2.6], "target": [0.9, 0.1, 0] },
	"grid": { "pos": [0.9, -0.75, 0.2], "size": [4, 3] },
	"fixtures": [{ "kind": "box", "pos": [0.35, -0.01, 0.1], "size": [0.9, 0.02, 0.6], "opacity": 0.5 }],
	"nodes": [
		{ "id": "T101", "kind": "tank", "tag": "T101", "label": "T-101", "pos": [1.0, -0.75, 0.4] },
		{ "id": "P101", "kind": "pump", "tag": "P101", "label": "P-101", "pos": [0.3, 0, 0.2] },
		{ "id": "XV101", "kind": "valve", "tag": "XV101", "pos": [1.8, 0.85, 0], "bind": { "cmd": "Demand" } }
	],
	"pipes": [{ "points": [[0.46, 0.08, 0.2], [1.0, 0.08, 0.4], [1.0, -0.33, 0.4]], "bind": { "flowing": "P101.Running" } }]
}
```

- **Positions are metres**, +y up, from the site's origin marker.
  `rot` is degrees `[x, y, z]`; `[0, 90, 0]` turns a node on the spot.
- **`tag`** is the struct tag the kind's component reads members off
  (`Level`, `Running`, `Pos`…). The built-in kinds read the UDTs in
  [`examples/spatial-rig/types.st`](../examples/spatial-rig/types.st).
- **`bind`** is the mimic's binding map, `prop -> ref`, with dotted paths:
  `"cmd": "Demand"`, `"speed": "P101.Speed"`, `"ok": "!P101.Fault"`. An
  absent tag leaves the prop unset. A bound prop overrides the member on the
  built-in kinds, so a project with flat tags (`T101_Level`) can use `tank`
  with no `tag` at all.
- **`pipes`** bind `flowing` (a boolean as-is, a number above 2 %).
- **`fixtures`** are static context (`box`, `plane`, `marker`); `grid` is
  a reference grid on a horizontal plane.
- **`writable`** is reserved and must be empty: the view is read-only until
  the write path ships through the kit's `writeTag` + `confirm()`.

- **`kinds`** is the kind ↔ UDT contract: `"kinds": { "pump": { "type": "VfdPump" } }`
  re-points a built-in kind at this project's UDT (members kept), and
  `"switch": { "type": "Switch", "members": ["PortsUp", "Fault"] }` declares
  a kind the app registers so `naut check` can hold it to its members.

`validateScene(doc, Object.keys(registry))` is pure and returns
`{ ok, errors: [{ path, message }] }`; `sceneTags(doc)` is the root-tag
list for the subscription.

## Nobody types a scene

The CLI generates and checks scene files against the manifest, the way it
does tag files:

```sh
naut scene init .    # one node per struct tag a kind can draw, on a grid, camera fitted
naut check .         # every *.scene.json at the project root: kinds, UDT types, members, refs
```

The VS Code extension ships a JSON Schema for `*.scene.json`, so completion
and the structural checks are there as you type. A person, a script or an
AI agent works the same loop: generate, check, fix the paths it is told.

## Kinds and the registry

| kind | reads | shows |
|---|---|---|
| `tank` | `Level` (%), `TempC` | fluid height inside a transparent shell |
| `pump` | `Running`, `Fault`, `Speed` (%) | the coupling turns at speed; body green while running |
| `valve` | `Pos` (%), `Cmd` (%) | a quarter-turn handle following `Pos` |

Equipment is grey when normal (ISA-101): colour means running, flowing,
or an alarm. There are two ways to add a kind.

### A kind as data: a glTF and a few drives

A `kinds` entry with a `model` is a kind the document brings — no Svelte.
The model's **named meshes** are what the `drive` list moves from the
node's struct:

```json
"kinds": {
	"pump": {
		"model": "models/pump.glb",
		"drive": [
			{ "mesh": "Coupling", "spin": { "axis": "x", "revPerS": { "bind": "Speed", "scale": 0.02 } } },
			{ "mesh": "Motor", "tint": { "bind": "Running", "on": "running" } }
		]
	},
	"beacon": {
		"type": "Switch", "model": "models/beacon.glb", "status": "{PortsUp} up",
		"drive": [{ "mesh": "Lamp", "emissive": { "bind": "Fault", "on": "critical" } }]
	}
}
```

- **`model`** is a URL path from the app root (`hmi/static/models/pump.glb`
  serves at `/models/pump.glb`), metres, +y up; the node's `pos`/`rot`/`scale`
  place its origin. The package ships the three built-ins as models in
  `models/` (generated by `models/build.py` with Blender) with their
  `kinds.json` — copy what you use into `static/`.
- **Drives**, one mesh and one channel each: `spin` (`axis`, `revPerS`),
  `turn` (`axis`, `deg`), `scale` (`axis` or `xyz`, `to`), `tint` (`on`,
  `off?`), `emissive` (`on`, `intensity?`), `visible`. A number is
  `{ bind, scale?, offset?, min?, max? }`; a boolean is `{ bind }` — `true`,
  or a number above `threshold`, `!` negates. `bind` names a member of the
  node's struct; the node's own `bind` map overrides it (`speed` overrides
  `Speed`). Colours are palette slots (`running`, `fluid`, `handle`,
  `stale`, a priority) or CSS colours.
- **`status`** is the label text as a template: `{Level:1} %`,
  `{Running?run:stopped}`. `bounds` is `"auto"` (the model's box) or
  `{ size, center }`; `labelAt` defaults to the top of the bounds.
- A data kind under a **built-in's name** (`pump` with a `model`) keeps its
  contract, status text and faceplate and replaces the geometry. Without a
  `model`, or when the model fails to load, the built-in Svelte kind renders,
  so a project with no assets still renders.
- The members a kind reads are `members` plus what its drives and status
  name; `naut check` holds every node to that, and checks the files exist.
  A drive naming a mesh the model lacks is a console warning.

### A kind as a component

The escape hatch for behaviour the vocabulary cannot express — extend the
registry; nothing in the renderer names a kind:

```ts
import { builtinRegistry, type NodeKindDef, type NodeProps } from '@joyautomation/nautilus-hmi-3d';
import Switch3D from '$lib/Switch3D.svelte';

const switchKind: NodeKindDef = {
	component: Switch3D as Component<NodeProps>,
	bounds: { size: [0.45, 0.05, 0.3], center: [0, 0.025, 0] },
	labelAt: [0, 0.1, 0],
	status: (v, good) => (good ? `${(v as { PortsUp?: number })?.PortsUp ?? 0} up` : 'stale')
};
<SceneView {doc} {rt} {alarms} registry={{ ...builtinRegistry, switch: switchKind }} />
```

A kind's component receives `value` (the struct), `good`, `label`,
`selected`, then the node's static `props` and resolved `bind` props.

## Surroundings and the look

```json
"environment": { "hdri": "env/workshop_1k.hdr", "backdrop": "env/workshop_6k.jpg", "background": "ground", "floor": -0.75, "intensity": 0.45, "shadows": true, "fog": { "near": 4, "far": 14 } },
"fixtures": [{ "kind": "plane", "pos": [0.9, -0.749, 0.2], "size": [4, 3],
	"texture": { "map": "textures/concrete_diff_1k.jpg", "normalMap": "textures/concrete_nor_gl_1k.jpg", "roughnessMap": "textures/concrete_rough_1k.jpg", "repeat": [4, 3] } }]
```

An `environment` lights the scene with an HDRI (`.hdr` / `.exr`; a CC0 1k
file from Poly Haven is ~1.7 MB) — `background` `none` (default), `sky`,
or `ground` (projected onto a floor at `floor`, so a desk-scale scene stands
in the room); `intensity` scales its light on the models (a bright HDRI at 1
swamps the key light; turn it down for visible shadows); `shadows` adds a
shadow-casting key light with soft shadows. `backdrop` is a separate,
sharper equirect for what the eye sees (a tonemapped 4k–6k JPG of the
same photo; the 1k HDR only lights), and `fog` fades the far backdrop and
the projected floor toward a colour so their softness is deliberate.
Keep the big files out of git: the example fetches them from a GitHub
release by a manifest with a sha256 per file. A `plane` fixture takes PBR
maps. The built-ins get metalness/roughness in this look, colours unchanged.

`<SceneView look="flat">` renders the same document as Milestone 1 did —
primitives, three lights, no assets — for a project without assets, a
low-end tier, or a before/after. `bind:look` and a button in the `hud`
snippet flip it live (`examples/spatial-rig/hmi` does).

The glTF loader and the HDRI loaders are **dynamic imports**: a document
without a `model` or an `environment` never fetches them, and the base
bundle stays where it was.

## Linking the package locally

`examples/spatial-rig/hmi` depends on this package by `file:` link. A
linked package resolves its own `node_modules` first, and two copies of
`svelte` cannot share context (nor two of `three` an `instanceof`), so a
consumer that links it must dedupe in `vite.config.ts`:

```ts
resolve: { dedupe: ['svelte', 'three', '@threlte/core', '@threlte/extras', '@joyautomation/nautilus-hmi'] }
```

and build the package first (`npm run package` → `dist/`).

## Measuring

`<SceneView perf>` shows the frame rate and two p95 latencies: **rx→pixel**,
a frame's arrival in the browser to the animation frame after the one that
drew it (the render cost, honest from any device), and **ctrl→pixel**, the
controller's timestamp to the same pixel, which adds the network hop but
needs the two clocks to agree; when they don't, the HUD says by how much
instead. Measure in a **visible** window: a hidden tab throttles
`requestAnimationFrame` to ~1 Hz and the numbers mean nothing.

## Development

```sh
npm install
npm test          # scene validation, bindings, drives, the alarm fold, the kinds, the schema/kinds.json sync
npm run check     # svelte-check
npm run package   # -> dist/, publint
```

## License

Apache-2.0
