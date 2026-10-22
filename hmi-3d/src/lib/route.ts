// An orthogonal run between two ports (docs/design/spatial-hmi.md §3g,
// slice 3): what the scene editor suggests when a pipe is drawn port to
// port, and what a person then tweaks. Leave each port along its
// direction, rise to a header height, run the two horizontal legs, and
// drop — the mimic's autoroute with a third axis. Pure; three-free.
import type { Vec3 } from './scene.js';

export type RouteEnd = { at: Vec3; dir: Vec3 };

const mm = (v: number): number => Math.round(v * 1000) / 1000 + 0;
const same = (a: Vec3, b: Vec3): boolean => a.every((c, i) => Math.abs(c - b[i]) < 1e-6);
/** b sits on the straight line through a and c (the middle of three). */
function collinear(a: Vec3, b: Vec3, c: Vec3): boolean {
	const u = [b[0] - a[0], b[1] - a[1], b[2] - a[2]];
	const v = [c[0] - b[0], c[1] - b[1], c[2] - b[2]];
	const cross = [u[1] * v[2] - u[2] * v[1], u[2] * v[0] - u[0] * v[2], u[0] * v[1] - u[1] * v[0]];
	const dot = u[0] * v[0] + u[1] * v[1] + u[2] * v[2];
	return Math.hypot(...cross) < 1e-6 && dot >= 0;
}

/** The run's interior points, port to port (the ports themselves are the
 * anchored ends and are not returned). `lead` is how far a pipe leaves a
 * port along its direction before turning; `rise` how far above the
 * higher lead the header runs — 0 when the two leads already share a
 * height and the run can go straight. */
export function routePorts(a: RouteEnd, b: RouteEnd, { lead = 0.1, rise = 0.15 } = {}): Vec3[] {
	const p1: Vec3 = [a.at[0] + a.dir[0] * lead, a.at[1] + a.dir[1] * lead, a.at[2] + a.dir[2] * lead];
	const p2: Vec3 = [b.at[0] + b.dir[0] * lead, b.at[1] + b.dir[1] * lead, b.at[2] + b.dir[2] * lead];
	const level = Math.abs(p1[1] - p2[1]) < 1e-6 && (Math.abs(p1[0] - p2[0]) < 1e-6 || Math.abs(p1[2] - p2[2]) < 1e-6);
	const h = level ? p1[1] : Math.max(p1[1], p2[1]) + rise;
	const raw: Vec3[] = [a.at, p1, [p1[0], h, p1[2]], [p2[0], h, p1[2]], [p2[0], h, p2[2]], p2, b.at];
	// Drop repeats and the middles of straight stretches.
	const pts: Vec3[] = [];
	for (const p of raw) if (!pts.length || !same(pts[pts.length - 1], p)) pts.push(p);
	const out: Vec3[] = [];
	for (let i = 0; i < pts.length; i++) {
		if (i > 0 && i < pts.length - 1 && collinear(out[out.length - 1] ?? pts[i - 1], pts[i], pts[i + 1])) continue;
		out.push(pts[i]);
	}
	return out.slice(1, -1).map((p) => [mm(p[0]), mm(p[1]), mm(p[2])] as Vec3);
}
