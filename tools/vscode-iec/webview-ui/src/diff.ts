// Diff merge — a TypeScript port of the overlay logic from media/fbd.js.
// Node ids are stable diff keys by construction (they encode role + name +
// argument position), so matching is a straight id join: "changed" = same id
// with different label/type/wire; edges match on endpoints and change on
// negation or wire label.
import type { FbdModel, FbdNode, FbdEdge } from './layout';

export function mergeDiff(base: FbdModel, head: FbdModel): FbdModel {
	const bn = new Map(base.nodes.map((n) => [n.id, n]));
	const hn = new Map(head.nodes.map((n) => [n.id, n]));
	const nodes: FbdNode[] = [];
	for (const n of head.nodes) {
		const b = bn.get(n.id);
		const m: FbdNode = { ...n, inputs: [...(n.inputs ?? [])], outputs: [...(n.outputs ?? [])] };
		if (!b) {
			m.status = 'added';
		} else {
			for (const p of b.inputs ?? []) if (!m.inputs!.includes(p)) m.inputs!.push(p);
			for (const p of b.outputs ?? []) if (!m.outputs!.includes(p)) m.outputs!.push(p);
			m.status =
				b.label !== n.label || (b.type ?? '') !== (n.type ?? '') || (b.wire ?? '') !== (n.wire ?? '')
					? 'changed'
					: 'same';
		}
		nodes.push(m);
	}
	const headMax = Math.max(0, ...head.nodes.map((n) => n.layer));
	for (const n of base.nodes) {
		if (hn.has(n.id)) continue;
		nodes.push({ ...n, status: 'removed', layer: Math.min(n.layer, headMax) });
	}
	const key = (e: FbdEdge) => [e.from, e.fromPin ?? '', e.to, e.toPin ?? ''].join('→');
	const be = new Map(base.edges.map((e) => [key(e), e]));
	const he = new Map(head.edges.map((e) => [key(e), e]));
	const edges: FbdEdge[] = [];
	for (const e of head.edges) {
		const b = be.get(key(e));
		edges.push({
			...e,
			status: !b
				? 'added'
				: !!b.negated !== !!e.negated || (b.wire ?? '') !== (e.wire ?? '')
					? 'changed'
					: 'same',
		});
	}
	for (const e of base.edges) if (!he.has(key(e))) edges.push({ ...e, status: 'removed' });
	return { name: head.name || base.name, nodes, edges };
}
