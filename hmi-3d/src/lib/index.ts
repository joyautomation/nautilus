// @joyautomation/nautilus-hmi-3d — the spatial layer of the nautilus HMI
// kit: a live 3D process view rendered from a *.scene.json document bound
// to struct tags, on Threlte. Brief: docs/design/spatial-hmi.md.

// The view. `SceneView` is the whole page-level component (canvas, HUD,
// inspector); `Scene3D` is the renderer alone, for an app that owns its
// own <Canvas> (a scene beside other 3D content, or its own camera).
export { default as SceneView } from './components/SceneView.svelte';
export { default as Scene3D } from './components/Scene3D.svelte';
export { default as AssetDrawer } from './components/AssetDrawer.svelte';
export { default as PerfHud } from './components/PerfHud.svelte';

// Svelte authoring (docs/design/spatial-hmi.md §3d): the document's node
// and pipe as components, inside <Scene3D> or inside a component that
// defines a kind. A <Node> inside a <Node> is a part.
export { default as Node } from './components/Node.svelte';
export { default as Pipe } from './components/Pipe.svelte';
export { SCENE, NODE } from './context.js';
export type { SceneContext, NodeContext, PlacedNode } from './context.js';

// The built-in models and decorations, for a registry extension that
// composes them (a pump on a different skid) or a custom scene.
export { default as Tank3D } from './components/Tank3D.svelte';
export { default as Pump3D } from './components/Pump3D.svelte';
export { default as Valve3D } from './components/Valve3D.svelte';
export { default as Pipe3D } from './components/Pipe3D.svelte';
export { default as Halo } from './components/Halo.svelte';
export { default as Label } from './components/Label.svelte';
export { default as Fixture3D } from './components/Fixture3D.svelte';
// GltfNode and Surroundings are deliberately NOT exported here: Scene3D
// dynamic-imports them, and a static export would fold the glTF and HDRI
// loaders back into the base bundle (§3c, §6).

// The scene document: types, the validator, and the subscription list.
export { validateScene, sceneTags, isBindingRef, isAssetPath, isComponentPath, isMemberName, refRoot, kindMembers, assemblyMembers, kindDefinedBy } from './scene.js';
export type {
	SceneDoc,
	SceneKind,
	SceneAssembly,
	SceneNode,
	ScenePipe,
	SceneFixture,
	SceneTexture,
	SceneEnvironment,
	SceneGrid,
	SceneCamera,
	SceneError,
	Vec3
} from './scene.js';

// Kinds as data: the drive vocabulary, evaluated from the document.
export { DRIVE_CHANNELS, evalDrives, evalNum, evalBool, driveMembers, validateDrives, formatStatus, statusMembers, propFor } from './drives.js';
export type { Drive, DriveChannel, MeshState, NumExpr, BoolExpr, Axis } from './drives.js';

// Bindings: the mimic's grammar plus dotted paths, evaluated from the doc.
export { resolveNodeBindings, resolveRefs, readPart, bindingsGood, readPath, member, num, flowing } from './bindings.js';

// The registry: extend `builtinRegistry` with your own kinds.
export { builtinRegistry, registryFor, tankKind, pumpKind, valveKind, kindOf, componentRegistry, matchModule, assemblyBounds } from './registry.js';
export type { NodeKindDef, NodeProps, PanelProps, NodeRegistry, KindMeta, Box } from './registry.js';
export { BUILTIN_CONTRACT, tankState, tankStatus, pumpState, pumpStatus, valveState, valveStatus } from './kinds.js';

// Alarms in space: one worst-priority entry per asset.
export { worstAlarmByAsset, alarmAsset } from './alarms.js';
export type { AssetAlarm, AlarmLike } from './alarms.js';

// Colours from the theme, and the measurement sampler behind PerfHud.
export { DEFAULT_PALETTE, paletteFromTheme } from './palette.js';
export type { Palette } from './palette.js';
export { PerfSampler, PLAUSIBLE_E2E_MS } from './perf.svelte.js';
