// The node registry: kind -> how to draw it, where its label and halo go,
// what its label says, and which 2D faceplate the inspector shows. Nothing
// in <Scene3D> names a kind; it looks every node up here. An app extends
// the built-ins by spreading:
//
//   registry={{ ...builtinRegistry, server: serverKind, 'switch-port': portKind }}
//
// which is how the IT-hardware kinds arrive without touching this package —
// or, for a kind a component file defines (§3d), with kindOf() or by
// handing SceneView its modules. The pure half lives in defs.ts.
import type { Component } from 'svelte';
import { tankStatus, pumpStatus, valveStatus, BUILTIN_CONTRACT } from './kinds.js';
import type { NodeKindDef, NodeProps, NodeRegistry } from './defs.js';
import Tank3D from './components/Tank3D.svelte';
import Pump3D from './components/Pump3D.svelte';
import Valve3D from './components/Valve3D.svelte';
import TankPanel from './components/TankPanel.svelte';
import PumpPanel from './components/PumpPanel.svelte';
import ValvePanel from './components/ValvePanel.svelte';

export type { NodeProps, PanelProps, NodeKindDef, NodeRegistry, Box, KindMeta } from './defs.js';
export { kindOf, matchModule, componentRegistry, assemblyBounds, registryFor } from './defs.js';

export const tankKind: NodeKindDef = {
	component: Tank3D as Component<NodeProps>,
	...BUILTIN_CONTRACT.tank,
	bounds: { size: [0.5, 0.52, 0.5], center: [0, 0.2, 0] },
	labelAt: [0, 0.5, 0],
	status: tankStatus,
	panel: TankPanel
};

export const pumpKind: NodeKindDef = {
	component: Pump3D as Component<NodeProps>,
	...BUILTIN_CONTRACT.pump,
	bounds: { size: [0.42, 0.24, 0.2], center: [0, 0.09, 0] },
	labelAt: [0, 0.24, 0],
	status: pumpStatus,
	panel: PumpPanel
};

export const valveKind: NodeKindDef = {
	component: Valve3D as Component<NodeProps>,
	...BUILTIN_CONTRACT.valve,
	bounds: { size: [0.16, 0.22, 0.22], center: [0, 0, 0.03] },
	labelAt: [0, 0.16, 0],
	status: valveStatus,
	panel: ValvePanel
};

export const builtinRegistry: NodeRegistry = { tank: tankKind, pump: pumpKind, valve: valveKind };
