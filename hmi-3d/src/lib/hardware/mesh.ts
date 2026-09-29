// The mesh view (docs/design/spatial-hmi.md §3e): the same topology as the
// rack, drawn as a network — devices as nodes floating in space, links as
// edges — so what connects to what reads at a glance. The layout is
// layered and deterministic, not a force simulation, so it never moves
// between visits: whatever is outside the model (the site) on top, the
// switches on a ring, the servers on a ring below, each server turned to
// sit under the switches it is cabled to. Pure, like rack.ts.
import type { Vec3 } from '../scene.js';
import { deviceById, parseEnd, type Topology, type TopoLink } from './topology.js';

export interface MeshNode {
	/** Topology device id, or a far end outside the model (`site`). */
	id: string;
	kind: string;
	pos: Vec3;
}

export interface MeshEdge {
	link: TopoLink;
	/** Index into the topology's links. */
	index: number;
	a: string;
	b: string;
	/** The edge's path: its ends and a control point bowed out so links
	 * between the same pair stay apart. */
	points: [Vec3, Vec3, Vec3];
}

const TAU = Math.PI * 2;
const on = (r: number, y: number, a: number): Vec3 => [r * Math.sin(a), y, r * Math.cos(a)];

export function meshLayout(t: Topology, opts: { radius?: number; serverRadius?: number; switchY?: number; serverY?: number; outsideY?: number } = {}): { nodes: MeshNode[]; edges: MeshEdge[] } {
	// The server ring wider than the switch ring, so an uplink leans out and
	// the rings never hide each other from above.
	const { radius = 0.5, serverRadius = 0.85, switchY = 1.15, serverY = 0.3, outsideY = 1.7 } = opts;
	const switches = t.devices.filter((d) => d.kind === 'switch');
	const servers = t.devices.filter((d) => d.kind !== 'switch');
	const angle = new Map<string, number>();
	switches.forEach((d, i) => angle.set(d.id, (i / Math.max(switches.length, 1)) * TAU));
	const isSwitch = new Set(switches.map((d) => d.id));

	// Each server faces the switches it is cabled to: the circular mean of
	// their angles (a server on no switch keeps its place in the order).
	const peers = (id: string) =>
		new Set(
			t.links.flatMap((l) => {
				const a = parseEnd(l.a).device;
				const b = parseEnd(l.b).device;
				const other = a === id ? b : b === id ? a : undefined;
				return other && isSwitch.has(other) ? [other] : [];
			})
		);
	const taken: number[] = [];
	servers.forEach((d, i) => {
		const ps = [...peers(d.id)];
		let a = (i / Math.max(servers.length, 1)) * TAU + Math.PI / Math.max(servers.length, 1);
		if (ps.length) {
			const x = ps.reduce((s, p) => s + Math.sin(angle.get(p)!), 0);
			const z = ps.reduce((s, p) => s + Math.cos(angle.get(p)!), 0);
			if (Math.hypot(x, z) > 1e-6) a = Math.atan2(x, z);
		}
		// Two servers on the same pair of switches: step the second aside.
		while (taken.some((b) => Math.abs(Math.atan2(Math.sin(a - b), Math.cos(a - b))) < 0.3)) a += 0.45;
		taken.push(a);
		angle.set(d.id, a);
	});

	const nodes: MeshNode[] = [
		...switches.map((d) => ({ id: d.id, kind: d.kind, pos: on(radius, switchY, angle.get(d.id)!) })),
		...servers.map((d) => ({ id: d.id, kind: d.kind, pos: on(serverRadius, serverY, angle.get(d.id)!) }))
	];
	// Far ends outside the model, above the device they hang off.
	for (const l of t.links) {
		for (const s of [l.a, l.b]) {
			const id = parseEnd(s).device;
			if (deviceById(t, id) || nodes.some((n) => n.id === id)) continue;
			const other = parseEnd(s === l.a ? l.b : l.a).device;
			const near = nodes.find((n) => n.id === other);
			nodes.push({ id, kind: 'outside', pos: near ? [near.pos[0] * 1.3, outsideY, near.pos[2] * 1.3] : [0, outsideY, 0] });
		}
	}

	const at = new Map(nodes.map((n) => [n.id, n.pos]));
	const pairCount = new Map<string, number>();
	const pairSeen = new Map<string, number>();
	const key = (l: TopoLink) => [parseEnd(l.a).device, parseEnd(l.b).device].sort().join('|');
	for (const l of t.links) pairCount.set(key(l), (pairCount.get(key(l)) ?? 0) + 1);
	const edges: MeshEdge[] = t.links.map((link, index) => {
		const a = parseEnd(link.a).device;
		const b = parseEnd(link.b).device;
		const pa = at.get(a)!;
		const pb = at.get(b)!;
		const k = key(link);
		const n = pairCount.get(k)!;
		const i = pairSeen.get(k) ?? 0;
		pairSeen.set(k, i + 1);
		// Bow each of n parallel links out from the pair's midline, sideways
		// and away from the centre, so they fan instead of overprinting.
		const mid: Vec3 = [(pa[0] + pb[0]) / 2, (pa[1] + pb[1]) / 2, (pa[2] + pb[2]) / 2];
		const d: Vec3 = [pb[0] - pa[0], pb[1] - pa[1], pb[2] - pa[2]];
		const len = Math.hypot(...d) || 1;
		// Sideways: across the edge, in the horizontal plane where it can be.
		let side: Vec3 = [-d[2], 0, d[0]];
		if (Math.hypot(...side) < 1e-6) side = [1, 0, 0];
		const sl = Math.hypot(...side);
		const off = (i - (n - 1) / 2) * 0.09 * len;
		const out = Math.hypot(mid[0], mid[2]) > 1e-6 ? 0.04 : 0;
		const r = Math.hypot(mid[0], mid[2]) || 1;
		const ctrl: Vec3 = [mid[0] + (side[0] / sl) * off + (mid[0] / r) * out, mid[1] + (side[1] / sl) * off, mid[2] + (side[2] / sl) * off + (mid[2] / r) * out];
		return { link, index, a, b, points: [pa, ctrl, pb] };
	});
	return { nodes, edges };
}
