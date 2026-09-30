// The contexts Svelte authoring reads (docs/design/spatial-hmi.md §3d).
// <Scene3D> provides the scene — the frame's tags, quality, the alarm fold,
// the effective registry, the selection — so that a <Node> anywhere below
// it, in a page or inside a component that defines a kind, is handed
// nothing. A <Node> provides its own struct to the <Node>s inside it, which
// is what makes a part a part.
import type { AssetAlarm } from './alarms.js';
import type { NodeKindDef, NodeRegistry, Box } from './defs.js';

export const SCENE = 'hmi3d:scene';
export const NODE = 'hmi3d:node';

/** A node the scene knows about — what the inspector drawer needs. */
export interface PlacedNode {
	id: string;
	tag?: string;
	kind?: string;
	label: string;
}

export interface SceneContext {
	/** The frame's tag map. */
	readonly tags: Record<string, unknown>;
	isGood(tag: string): boolean;
	/** Worst active alarm per asset (worstAlarmByAsset). */
	readonly alarms: Map<string, AssetAlarm>;
	/** The registry with the document's kinds laid on (registryFor). */
	readonly registry: NodeRegistry;
	readonly selected: string | null;
	/** The pickable node under the pointer (the frontmost one), or null. */
	readonly hovered: string | null;
	hover(id: string | null): void;
	/** The kind a node renders with: its registry entry, or the Svelte kind
	 * of the same name once its model has failed to load. */
	defFor(id: string | undefined, kind: string): NodeKindDef | undefined;
	/** A kind's box for this node: explicit, or what its model reported. */
	boundsOf(id: string | undefined, bounds: Box | 'auto'): Box;
	reportBounds(id: string | undefined, box: Box): void;
	reportFailed(id: string | undefined): void;
	pick(id: string): void;
	/** A right-click on a pickable node, with the browser's event (for
	 * where to put a menu). */
	contextPick(id: string, event: MouseEvent): void;
	/** Announce a placed node (for the drawer); returns the unregister. */
	register(node: PlacedNode): () => void;
}

/** What a <Node> provides to the <Node>s and <Pipe>s inside it. */
export interface NodeContext {
	readonly value: unknown;
	readonly good: boolean;
	/** The node's resolved `bind` props, which supply parts by member name. */
	readonly over: Record<string, unknown>;
	readonly id: string | undefined;
}
