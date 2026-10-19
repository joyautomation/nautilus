// Which node a click picks when several are stacked under the pointer.
// A tank is glass with a pump inside it; a click on the pump should pick
// the pump, not the shell in front of it — and a second click on the same
// spot should reach whatever is behind that.
import type { Material, Mesh, Object3D } from 'three';

/** One raycast hit, reduced to what picking needs: the node the hit
 * object belongs to (none for a floor or a gizmo) and whether its
 * surface is opaque. */
export type PickHit = { node: string | undefined; opaque: boolean };

/** A hit object the eye sees through: every material transparent with an
 * opacity below 1 (a tank's shell, the fluid in it, a placeholder box). */
export function seeThrough(o: Object3D): boolean {
	const m = (o as Mesh).material as Material | Material[] | undefined;
	if (!m) return false;
	const ms = Array.isArray(m) ? m : [m];
	return ms.length > 0 && ms.every((x) => x.transparent && x.opacity < 1);
}

/** The node a click picks, from the hits under the pointer front to back
 * and the node already selected. The natural target is the frontmost node
 * with an opaque surface — through a tank's glass to the pump inside —
 * or the frontmost node when every surface is see-through. When the
 * selected node is among the hits and it is not alone, the click goes to
 * the node behind it instead, so clicking the same spot again cycles
 * through everything stacked there. Null when no hit belongs to a node. */
export function resolvePick(hits: readonly PickHit[], selected: string | null | undefined): string | null {
	const order: string[] = [];
	const opaque = new Set<string>();
	for (const h of hits) {
		if (!h.node) continue;
		if (!order.includes(h.node)) order.push(h.node);
		if (h.opaque) opaque.add(h.node);
	}
	if (order.length === 0) return null;
	if (selected && order.length > 1) {
		const i = order.indexOf(selected);
		if (i >= 0) return order[(i + 1) % order.length];
	}
	return order.find((n) => opaque.has(n)) ?? order[0];
}
