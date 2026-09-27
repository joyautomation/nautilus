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

// The built-in models and decorations, for a registry extension that
// composes them (a pump on a different skid) or a custom scene.
export { default as Tank3D } from './components/Tank3D.svelte';
export { default as Pump3D } from './components/Pump3D.svelte';
export { default as Valve3D } from './components/Valve3D.svelte';
export { default as Pipe3D } from './components/Pipe3D.svelte';
export { default as Halo } from './components/Halo.svelte';
export { default as Label } from './components/Label.svelte';
export { default as Fixture3D } from './components/Fixture3D.svelte';

// The scene document: types, the validator, and the subscription list.
export { validateScene, sceneTags, isBindingRef, refRoot } from './scene.js';
export type { SceneDoc, SceneKind, SceneNode, ScenePipe, SceneFixture, SceneGrid, SceneCamera, SceneError, Vec3 } from './scene.js';

// Bindings: the mimic's grammar plus dotted paths, evaluated from the doc.
export { resolveNodeBindings, bindingsGood, readPath, member, num, flowing } from './bindings.js';

// The registry: extend `builtinRegistry` with your own kinds.
export { builtinRegistry, tankKind, pumpKind, valveKind } from './registry.js';
export type { NodeKindDef, NodeProps, PanelProps, NodeRegistry } from './registry.js';
export { tankState, tankStatus, pumpState, pumpStatus, valveState, valveStatus } from './kinds.js';

// Alarms in space: one worst-priority entry per asset.
export { worstAlarmByAsset, alarmAsset } from './alarms.js';
export type { AssetAlarm, AlarmLike } from './alarms.js';

// Colours from the theme, and the measurement sampler behind PerfHud.
export { DEFAULT_PALETTE, paletteFromTheme } from './palette.js';
export type { Palette } from './palette.js';
export { PerfSampler, PLAUSIBLE_E2E_MS } from './perf.svelte.js';
