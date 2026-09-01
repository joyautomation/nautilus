// A coordinate canvas as data — the shape every legacy HMI screen has.
//
// Ignition Perspective, FactoryTalk View and WinCC all draw a screen the same
// way: a FIXED plane of absolutely-placed symbols, scaled to whatever viewport
// it lands in. That is the right behaviour for a plant diagram — a pipe has to
// stay attached to the pump it feeds, so the schematic must never reflow, only
// grow and shrink. `CoordinateCanvas` renders one of these from a spec; this
// module is the spec's type and the hooks that adapt it to a given source.
//
// Nothing here knows what a node MEANS. `t` is an opaque discriminator the
// host app maps to its own components through a registry (or a leaf snippet),
// which is what keeps a transcriber for one legacy package from leaking into
// the kit.
import type { Component } from 'svelte';

/**
 * One node of a screen. A node carries whichever geometry its PARENT implies:
 * `x`/`y`/`w`/`h` under a coordinate container, `basis`/`grow`/`shrink` under
 * a flex one. Both map straight onto CSS.
 */
export interface CanvasNode {
	/** Component discriminator — looked up in the registry, or handed to the
	 *  `leaf` snippet. Container types (see `containers`) recurse instead. */
	t: string;
	/** Coordinate-container children: absolute px on the screen's own canvas. */
	x?: number;
	y?: number;
	w?: number;
	h?: number;
	/** Flex-container children: the flex item properties instead. */
	basis?: string | number;
	grow?: number;
	shrink?: number;
	/** Statically hidden in the source. */
	hidden?: boolean;
	/** The node's own props, passed through untouched. A few keys are read by
	 *  the renderer itself: `style` (an inline style object) and, on a
	 *  container, `direction` / `justify` / `alignItems` / `wrap`. */
	p?: Record<string, unknown>;
	/** Children, for a container node. */
	c?: CanvasNode[];
}

/** A whole screen: the canvas it was drawn on, and what is on it. */
export interface CanvasSpec {
	/** The design-time canvas width in px — everything is placed in these
	 *  units and the whole plane is scaled once to fit. */
	width: number;
	height: number;
	items: CanvasNode[];
}

/**
 * Node type → component. Each component receives `node` (the whole node, so it
 * can read `node.p` and its own box) and is rendered INSIDE the box the
 * renderer has already positioned and sized.
 */
export type CanvasRegistry = Record<string, Component<{ node: CanvasNode }>>;

/** The default container types: a coordinate plane and a flex row/column. */
export const CONTAINER_TYPES = ['coord', 'flex'];

/**
 * Turn a node's `p.style` object into an inline style string.
 * `{ fontSize: '18px' }` → `font-size:18px`.
 */
export function inlineStyle(node: CanvasNode): string {
	const s = (node.p?.style ?? {}) as Record<string, string>;
	return Object.entries(s)
		.map(([k, v]) => `${k.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())}:${v}`)
		.join(';');
}

/**
 * A node's flex-layout props, as CSS. Perspective names them `direction`,
 * `justify`, `alignItems` and `wrap`; they are the CSS properties under other
 * names, and without them every column lays itself out as a row. Applied to
 * every node, not just containers — a leaf that sets `justify` is laying out
 * its own contents.
 */
export function flexStyle(node: CanvasNode): string {
	const p = node.p ?? {};
	return (
		[
			['direction', 'flex-direction'],
			['justify', 'justify-content'],
			['alignItems', 'align-items'],
			['wrap', 'flex-wrap']
		] as const
	)
		.flatMap(([k, css]) => (typeof p[k] === 'string' ? [`${css}:${p[k] as string}`] : []))
		.join(';');
}

/** Walk a spec depth-first. Useful for counting points or collecting bindings. */
export function walkSpec(spec: CanvasSpec, visit: (node: CanvasNode) => void): void {
	const go = (n: CanvasNode) => {
		visit(n);
		n.c?.forEach(go);
	};
	spec.items.forEach(go);
}
