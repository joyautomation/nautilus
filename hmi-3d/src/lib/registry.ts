// The node registry: kind -> how to draw it, where its label and halo go,
// what its label says, and which 2D faceplate the inspector shows. Nothing
// in <Scene3D> names a kind; it looks every node up here. An app extends
// the built-ins by spreading:
//
//   registry={{ ...builtinRegistry, server: serverKind, 'switch-port': portKind }}
//
// which is how the IT-hardware kinds arrive without touching this package.
import type { Component } from 'svelte';
import type { Vec3 } from './scene.js';
import { tankStatus, pumpStatus, valveStatus } from './kinds.js';
import Tank3D from './components/Tank3D.svelte';
import Pump3D from './components/Pump3D.svelte';
import Valve3D from './components/Valve3D.svelte';
import TankPanel from './components/TankPanel.svelte';
import PumpPanel from './components/PumpPanel.svelte';
import ValvePanel from './components/ValvePanel.svelte';

/** What every kind's component receives. Static `props` and resolved `bind`
 * props are spread on top (bind wins), so a component declares the extras
 * it understands and ignores the rest. */
export interface NodeProps {
	/** The node's struct tag, whole, or undefined (no tag, or not yet received). */
	value: unknown;
	/** Quality of the node's tag AND of every bound ref's root. */
	good: boolean;
	label: string;
	selected: boolean;
	[prop: string]: unknown;
}

export interface PanelProps {
	value: unknown;
	label: string;
	good: boolean;
}

export interface NodeKindDef {
	component: Component<NodeProps>;
	/** The UDT this kind reads by default, and the members it reads — the
	 * contract `naut check` and `naut scene init` work from. A scene's
	 * `kinds` block re-points `type` for a project whose UDT is named
	 * differently. */
	type?: string;
	members?: string[];
	/** Local-space box for the alarm halo and the selection outline. */
	bounds: { size: Vec3; center: Vec3 };
	/** Where the floating label sits, local space. */
	labelAt: Vec3;
	/** The label's value text. Pure, so it is testable and matches the panel. */
	status?: (value: unknown, good: boolean) => string;
	/** The 2D faceplate the inspector drawer shows; absent = the member table only. */
	panel?: Component<PanelProps>;
}

export type NodeRegistry = Record<string, NodeKindDef>;

export const tankKind: NodeKindDef = {
	component: Tank3D as Component<NodeProps>,
	type: 'Tank',
	members: ['Level', 'TempC'],
	bounds: { size: [0.5, 0.52, 0.5], center: [0, 0.2, 0] },
	labelAt: [0, 0.5, 0],
	status: tankStatus,
	panel: TankPanel
};

export const pumpKind: NodeKindDef = {
	component: Pump3D as Component<NodeProps>,
	type: 'Motor',
	members: ['Running', 'Fault', 'Speed'],
	bounds: { size: [0.42, 0.24, 0.2], center: [0, 0.09, 0] },
	labelAt: [0, 0.24, 0],
	status: pumpStatus,
	panel: PumpPanel
};

export const valveKind: NodeKindDef = {
	component: Valve3D as Component<NodeProps>,
	type: 'Valve',
	members: ['Pos', 'Cmd'],
	bounds: { size: [0.16, 0.22, 0.22], center: [0, 0, 0.03] },
	labelAt: [0, 0.16, 0],
	status: valveStatus,
	panel: ValvePanel
};

export const builtinRegistry: NodeRegistry = { tank: tankKind, pump: pumpKind, valve: valveKind };
