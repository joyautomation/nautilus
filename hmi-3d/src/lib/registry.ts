// The node registry: kind -> how to draw it, where its label and halo go,
// what its label says, and which 2D faceplate the inspector shows. Nothing
// in <Scene3D> names a kind; it looks every node up here. An app extends
// the built-ins by spreading:
//
//   registry={{ ...builtinRegistry, server: serverKind, 'switch-port': portKind }}
//
// which is how the IT-hardware kinds arrive without touching this package.
import type { Component } from 'svelte';
import type { SceneDoc, SceneKind, Vec3 } from './scene.js';
import { formatStatus } from './drives.js';
import { tankStatus, pumpStatus, valveStatus, BUILTIN_CONTRACT } from './kinds.js';
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
	/** A data kind (docs/design/spatial-hmi.md §3c): the document's entry,
	 * handed to the component that loads the model and applies the drives. */
	data?: SceneKind;
	/** The UDT this kind reads by default, and the members it reads — the
	 * contract `naut check` and `naut scene init` work from. A scene's
	 * `kinds` block re-points `type` for a project whose UDT is named
	 * differently. */
	type?: string;
	members?: string[];
	/** Local-space box for the alarm halo and the selection outline.
	 * `'auto'` on a data kind: the model reports its box once loaded. */
	bounds: { size: Vec3; center: Vec3 } | 'auto';
	/** Where the floating label sits, local space; on a data kind, absent =
	 * the top centre of the bounds. */
	labelAt?: Vec3;
	/** The label's value text. Pure, so it is testable and matches the panel. */
	status?: (value: unknown, good: boolean) => string;
	/** The 2D faceplate the inspector drawer shows; absent = the member table only. */
	panel?: Component<PanelProps>;
}

export type NodeRegistry = Record<string, NodeKindDef>;

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

/**
 * The registry a document renders with: the given registry, with the
 * document's data kinds (a `kinds` entry with a `model`) laid on top. A
 * data kind under a built-in's name keeps that built-in's contract, status
 * text and faceplate and replaces the geometry; a new name gets the
 * drawer's member table and, if it has a `status` template, that text.
 * `GltfNode` is the component for every one of them; it is passed in so
 * this module never imports it (it is a dynamic import, §3c).
 */
export function registryFor(doc: SceneDoc, registry: NodeRegistry, gltf: Component<NodeProps>): NodeRegistry {
	const out: NodeRegistry = { ...registry };
	for (const [name, k] of Object.entries(doc.kinds ?? {})) {
		if (!k.model) continue;
		const base = registry[name];
		const tpl = k.status;
		out[name] = {
			component: gltf,
			data: k,
			type: k.type ?? base?.type,
			members: k.members ?? base?.members,
			bounds: k.bounds && k.bounds !== 'auto' ? k.bounds : 'auto',
			labelAt: k.labelAt,
			status: tpl ? (value, good) => formatStatus(tpl, value, good) : base?.status,
			panel: base?.panel
		};
	}
	return out;
}
