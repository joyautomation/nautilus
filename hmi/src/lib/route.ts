// Pipes the screen routes itself. A mimic pipe with `routing: "auto"`
// says what it connects (an equipment port, or another pipe it tees into)
// and the screen draws the line: square, clear of every piece of
// equipment and every label, with as few bends and crossings as it can,
// and never along another pipe. Both renderers (the runtime <Mimic> and
// the VS Code mimic editor) call routePipes() with what they measured, so
// a routed pipe draws the same in each.
//
// How: a grid search. The canvas is a lattice of lines every ROUTE_GRID
// units, plus the exact lines every port, waypoint and already-drawn
// pipe sits on (so a drop from a valve runs straight down its own x, and
// a tee lands exactly on its header). A move along the lattice costs its
// length; a bend, a crossing and running close beside another pipe cost
// more; equipment, labels and another pipe's run are walls. Pipes are
// routed one at a time in the document's order (a pipe that tees into
// another after that one), each seeing the ones before it.
import type { PortDir } from './mimic.js';

export type Pt = [number, number];
export interface Rect {
	x: number;
	y: number;
	w: number;
	h: number;
}
/** A routed pipe's end: a port (its position, the way it faces, and which
 * obstacle is its own equipment, which the line leaves straight out of),
 * or another pipe, which it tees into wherever is best. */
export type RouteEnd = { x: number; y: number; dir?: PortDir; own?: number } | { pipe: string };
export interface RouteJob {
	id: string;
	from: RouteEnd;
	to: RouteEnd;
	/** Points the line passes through, in order. */
	via?: Pt[];
}
export interface RouteInput {
	width: number;
	height: number;
	/** Equipment boxes and labels, in canvas units. */
	obstacles: Rect[];
	/** Pipes already drawn (their own points), which routed ones avoid. */
	fixed: { id: string; pts: Pt[] }[];
	jobs: RouteJob[];
}
export interface RouteResult {
	paths: Map<string, Pt[]>;
	/** Pipes with no clear way: drawn square and direct instead. */
	failed: string[];
}

/** The lattice's spacing, in canvas units. */
export const ROUTE_GRID = 10;
/** How far a line keeps from equipment and labels. */
export const ROUTE_CLEAR = 10;
/** Cost of a bend, as a length of straight line. */
export const ROUTE_BEND = 40;
/** Cost of crossing another pipe. */
export const ROUTE_CROSS = 150;
/** Lines closer than this alongside another pipe cost NEAR_COST a unit. */
export const ROUTE_NEAR = 15;
const NEAR_COST = 2;

const VEC: Record<PortDir, Pt> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] };
// direction indices: 0 right, 1 left, 2 down, 3 up, 4 none (a start with no way it faces)
const DX = [1, -1, 0, 0];
const DY = [0, 0, 1, -1];
const DIR_INDEX: Record<PortDir, number> = { right: 0, left: 1, down: 2, up: 3 };
const OPPOSITE = [1, 0, 3, 2];

const isPort = (e: RouteEnd): e is { x: number; y: number; dir?: PortDir; own?: number } => !('pipe' in e);

/** Simplify a polyline: drop repeated points and points in a straight run. */
export function simplify(pts: Pt[]): Pt[] {
	const out: Pt[] = [];
	for (const p of pts) {
		const q = out[out.length - 1];
		if (q && Math.abs(q[0] - p[0]) < 0.01 && Math.abs(q[1] - p[1]) < 0.01) continue;
		out.push(p);
		while (out.length >= 3) {
			const [a, b, c] = out.slice(-3);
			const straight = (Math.abs(a[0] - b[0]) < 0.01 && Math.abs(b[0] - c[0]) < 0.01) || (Math.abs(a[1] - b[1]) < 0.01 && Math.abs(b[1] - c[1]) < 0.01);
			if (!straight) break;
			out.splice(out.length - 2, 1);
		}
	}
	return out;
}

/** Where a line leaving a port clears its own equipment: straight out the
 * way the port faces until it is ROUTE_CLEAR past the box. */
function escape(e: { x: number; y: number; dir?: PortDir; own?: number }, obstacles: Rect[]): Pt {
	if (!e.dir) return [e.x, e.y];
	const [dx, dy] = VEC[e.dir];
	const r = e.own !== undefined ? obstacles[e.own] : undefined;
	let d = ROUTE_CLEAR + 2;
	if (r) {
		if (dx > 0) d = Math.max(d, r.x + r.w + ROUTE_CLEAR + 2 - e.x);
		if (dx < 0) d = Math.max(d, e.x - (r.x - ROUTE_CLEAR - 2));
		if (dy > 0) d = Math.max(d, r.y + r.h + ROUTE_CLEAR + 2 - e.y);
		if (dy < 0) d = Math.max(d, e.y - (r.y - ROUTE_CLEAR - 2));
	}
	return [e.x + dx * d, e.y + dy * d];
}

// A binary heap of [cost, state].
class Heap {
	c: number[] = [];
	s: number[] = [];
	push(cost: number, state: number) {
		const { c, s } = this;
		let i = c.length;
		c.push(cost);
		s.push(state);
		while (i > 0) {
			const p = (i - 1) >> 1;
			if (c[p] <= cost) break;
			c[i] = c[p];
			s[i] = s[p];
			i = p;
		}
		c[i] = cost;
		s[i] = state;
	}
	pop(): [number, number] | undefined {
		const { c, s } = this;
		if (!c.length) return undefined;
		const top: [number, number] = [c[0], s[0]];
		const lc = c.pop()!;
		const ls = s.pop()!;
		if (c.length) {
			let i = 0;
			for (;;) {
				const l = 2 * i + 1;
				if (l >= c.length) break;
				const r = l + 1;
				const m = r < c.length && c[r] < c[l] ? r : l;
				if (c[m] >= lc) break;
				c[i] = c[m];
				s[i] = s[m];
				i = m;
			}
			c[i] = lc;
			s[i] = ls;
		}
		return top;
	}
}

const uniq = (v: number[]) => {
	const s = v.map((x) => Math.round(x * 100) / 100).sort((a, b) => a - b);
	const out: number[] = [];
	for (const x of s) if (!out.length || x - out[out.length - 1] > 0.5) out.push(x);
	return out;
};
/** The first index with v[i] >= x. */
function lower(v: number[], x: number): number {
	let lo = 0,
		hi = v.length;
	while (lo < hi) {
		const m = (lo + hi) >> 1;
		if (v[m] < x) lo = m + 1;
		else hi = m;
	}
	return lo;
}
const near = (v: number[], x: number) => {
	const i = lower(v, x - 0.5);
	return i < v.length && Math.abs(v[i] - x) <= 0.5 ? i : -1;
};

/** Segments of drawn pipes, split by axis. */
interface Seg {
	pipe: number;
	a: number; // the fixed coordinate (y of a horizontal run, x of a vertical)
	lo: number;
	hi: number;
}
function segsOf(paths: Pt[][]): { h: Seg[]; v: Seg[] } {
	const h: Seg[] = [],
		v: Seg[] = [];
	paths.forEach((pts, pipe) => {
		for (let i = 1; i < pts.length; i++) {
			const [x0, y0] = pts[i - 1],
				[x1, y1] = pts[i];
			if (Math.abs(y0 - y1) < 0.5 && Math.abs(x0 - x1) >= 0.5) h.push({ pipe, a: y0, lo: Math.min(x0, x1), hi: Math.max(x0, x1) });
			else if (Math.abs(x0 - x1) < 0.5 && Math.abs(y0 - y1) >= 0.5) v.push({ pipe, a: x0, lo: Math.min(y0, y1), hi: Math.max(y0, y1) });
			// a slanted run (a hand-drawn pipe) is approximated by its two legs
			else if (Math.abs(x0 - x1) >= 0.5) {
				h.push({ pipe, a: y0, lo: Math.min(x0, x1), hi: Math.max(x0, x1) });
				v.push({ pipe, a: x1, lo: Math.min(y0, y1), hi: Math.max(y0, y1) });
			}
		}
	});
	return { h, v };
}

/** Route one pipe through the lattice. `drawn` are the pipes so far (by
 * index); `target`, when the end is a pipe, is that pipe's index. */
function routeOne(
	W: number,
	H: number,
	obstacles: Rect[],
	drawn: Pt[][],
	start: Pt,
	startDir: number,
	end: Pt | null,
	endDir: number,
	targetPipe: number,
	via: Pt[]
): Pt[] | null {
	const segs = segsOf(drawn);
	// the lattice
	const xsRaw: number[] = [],
		ysRaw: number[] = [];
	for (let x = ROUTE_GRID; x < W; x += ROUTE_GRID) xsRaw.push(x);
	for (let y = ROUTE_GRID; y < H; y += ROUTE_GRID) ysRaw.push(y);
	xsRaw.push(start[0]);
	ysRaw.push(start[1]);
	if (end) {
		xsRaw.push(end[0]);
		ysRaw.push(end[1]);
	}
	for (const p of via) {
		xsRaw.push(p[0]);
		ysRaw.push(p[1]);
	}
	for (const s of segs.h) ysRaw.push(s.a);
	for (const s of segs.v) xsRaw.push(s.a);
	const xs = uniq(xsRaw),
		ys = uniq(ysRaw);
	const NX = xs.length,
		NY = ys.length,
		N = NX * NY;
	const id = (i: number, j: number) => j * NX + i;

	// walls: nodes inside equipment or a label, and the runs between them
	const blocked = new Uint8Array(N); // bit 1 node, 2 run to the right, 4 run down
	for (const r of obstacles) {
		const x0 = r.x - ROUTE_CLEAR,
			x1 = r.x + r.w + ROUTE_CLEAR,
			y0 = r.y - ROUTE_CLEAR,
			y1 = r.y + r.h + ROUTE_CLEAR;
		const i0 = lower(xs, x0 + 0.01),
			i1 = lower(xs, x1 - 0.01); // xs strictly inside: [i0, i1)
		const j0 = lower(ys, y0 + 0.01),
			j1 = lower(ys, y1 - 0.01);
		for (let j = j0; j < j1; j++) {
			for (let i = i0; i < i1; i++) blocked[id(i, j)] |= 1;
			// runs to the right crossing the box's interior span on this row
			for (let i = Math.max(0, i0 - 1); i < Math.min(NX - 1, i1); i++) blocked[id(i, j)] |= 2;
		}
		for (let i = i0; i < i1; i++) for (let j = Math.max(0, j0 - 1); j < Math.min(NY - 1, j1); j++) blocked[id(i, j)] |= 4;
	}

	// other pipes: on a run (bit 1 horizontal, 2 vertical, 4 an end or a
	// corner), close alongside one (8 horizontal, 16 vertical), and the
	// runs between nodes a pipe already takes (32 to the right, 64 down)
	const onPipe = new Uint8Array(N);
	const onWhich = new Int16Array(N).fill(-1);
	for (const s of segs.h) {
		const j = near(ys, s.a);
		const i0 = lower(xs, s.lo - 0.5),
			i1 = lower(xs, s.hi + 0.5);
		if (j >= 0)
			for (let i = i0; i < i1; i++) {
				const end = Math.abs(xs[i] - s.lo) <= 0.5 || Math.abs(xs[i] - s.hi) <= 0.5;
				onPipe[id(i, j)] |= end ? 4 : 1;
				onWhich[id(i, j)] = s.pipe;
				if (i + 1 < i1) onPipe[id(i, j)] |= 32;
			}
		const k0 = lower(ys, s.a - ROUTE_NEAR + 0.01),
			k1 = lower(ys, s.a + ROUTE_NEAR - 0.01);
		for (let k = k0; k < k1; k++) if (k !== j) for (let i = i0; i < i1; i++) onPipe[id(i, k)] |= 8;
	}
	for (const s of segs.v) {
		const i = near(xs, s.a);
		const j0 = lower(ys, s.lo - 0.5),
			j1 = lower(ys, s.hi + 0.5);
		if (i >= 0)
			for (let j = j0; j < j1; j++) {
				const end = Math.abs(ys[j] - s.lo) <= 0.5 || Math.abs(ys[j] - s.hi) <= 0.5;
				onPipe[id(i, j)] |= end ? 4 : 2;
				onWhich[id(i, j)] = s.pipe;
				if (j + 1 < j1) onPipe[id(i, j)] |= 64;
			}
		const k0 = lower(xs, s.a - ROUTE_NEAR + 0.01),
			k1 = lower(xs, s.a + ROUTE_NEAR - 0.01);
		for (let k = k0; k < k1; k++) if (k !== i) for (let j = j0; j < j1; j++) onPipe[id(k, j)] |= 16;
	}

	const nodeOf = (p: Pt) => {
		const i = near(xs, p[0]),
			j = near(ys, p[1]);
		return i < 0 || j < 0 ? -1 : id(i, j);
	};
	const stops = [start, ...via];
	const s0 = nodeOf(start);
	if (s0 < 0 || blocked[s0] & 1) return null;
	let cur = s0,
		curDir = startDir;
	const out: Pt[] = [];
	for (let k = 1; k <= stops.length; k++) {
		const last = k === stops.length;
		const goal = last ? (end ? nodeOf(end) : -1) : nodeOf(stops[k]);
		if (!last && goal < 0) return null;
		const r = search(cur, curDir, goal, last && !end ? targetPipe : -1, last ? endDir : 4);
		if (!r) return null;
		out.push(...(out.length ? r.pts.slice(1) : r.pts));
		cur = r.node;
		curDir = r.dir;
	}
	return out;

	function search(src: number, srcDir: number, goal: number, tee: number, goalDir: number): { pts: Pt[]; node: number; dir: number } | null {
		const S = N * 5;
		const dist = new Float64Array(S).fill(Infinity);
		const prev = new Int32Array(S).fill(-1);
		const heap = new Heap();
		const st0 = src * 5 + srcDir;
		dist[st0] = 0;
		heap.push(0, st0);
		let best = -1,
			bestCost = Infinity;
		for (;;) {
			const top = heap.pop();
			if (!top) break;
			const [c, st] = top;
			if (c > dist[st] || c >= bestCost) {
				if (c >= bestCost) break;
				continue;
			}
			const node = (st / 5) | 0,
				d = st % 5;
			// arrived?
			if (node !== src) {
				if (node === goal) {
					// the last turn into the port's own stub
					const fin = c + (goalDir < 4 && d !== goalDir ? ROUTE_BEND : 0);
					if (fin < bestCost) {
						bestCost = fin;
						best = st;
					}
					continue;
				}
				if (tee >= 0 && onWhich[node] === tee && d < 4) {
					// onto the header across it, never along it
					const along = (onPipe[node] & 1 && d < 2) || (onPipe[node] & 2 && d >= 2);
					if (!along) {
						if (c < bestCost) {
							bestCost = c;
							best = st;
						}
						continue;
					}
				}
			}
			const i = node % NX,
				j = (node / NX) | 0;
			for (let nd = 0; nd < 4; nd++) {
				if (d < 4 && nd === OPPOSITE[d]) continue;
				const ni = i + DX[nd],
					nj = j + DY[nd];
				if (ni < 0 || nj < 0 || ni >= NX || nj >= NY) continue;
				const nn = id(ni, nj);
				if (blocked[nn] & 1 && nn !== goal) continue;
				// the run between: walls
				const runBit = nd < 2 ? 2 : 4;
				const runNode = nd === 0 ? node : nd === 1 ? nn : nd === 2 ? node : nn;
				if (blocked[runNode] & runBit) continue;
				// along another pipe: never; through its corner or end: never,
				// unless it is the header this one tees into
				const horiz = nd < 2;
				const pn = onPipe[nn];
				if (onPipe[runNode] & (horiz ? 32 : 64)) continue;
				const teeing = tee >= 0 && onWhich[nn] === tee;
				if (pn & 4 && !teeing && nn !== goal) continue;
				const len = horiz ? Math.abs(xs[ni] - xs[i]) : Math.abs(ys[nj] - ys[j]);
				let cost = c + len;
				if (d < 4 && d !== nd) cost += ROUTE_BEND;
				// across another pipe
				if ((horiz && pn & 2 && !(pn & 1)) || (!horiz && pn & 1 && !(pn & 2))) if (!teeing) cost += ROUTE_CROSS;
				// alongside one
				if ((horiz && pn & 8) || (!horiz && pn & 16)) cost += len * NEAR_COST;
				const ns = nn * 5 + nd;
				if (cost < dist[ns]) {
					dist[ns] = cost;
					prev[ns] = st;
					heap.push(cost, ns);
				}
			}
		}
		if (best < 0) return null;
		const pts: Pt[] = [];
		for (let st = best; st >= 0; st = prev[st]) {
			const node = (st / 5) | 0;
			pts.push([xs[node % NX], ys[(node / NX) | 0]]);
		}
		pts.reverse();
		return { pts, node: (best / 5) | 0, dir: best % 5 };
	}
}

/** Route every job. Pure: the same input gives the same lines. */
export function routePipes(input: RouteInput): RouteResult {
	const { width: W, height: H, obstacles } = input;
	const paths = new Map<string, Pt[]>();
	const drawn: Pt[][] = [];
	const index = new Map<string, number>();
	for (const f of input.fixed) {
		index.set(f.id, drawn.length);
		drawn.push(f.pts);
	}
	// a pipe that tees into another goes after it
	const jobs = input.jobs.slice();
	const byId = new Map(jobs.map((j) => [j.id, j]));
	const order: RouteJob[] = [];
	const state = new Map<string, number>(); // 1 visiting, 2 done
	const visit = (j: RouteJob) => {
		if (state.get(j.id)) return;
		state.set(j.id, 1);
		for (const e of [j.from, j.to]) if (!isPort(e) && byId.has(e.pipe) && e.pipe !== j.id && !state.get(e.pipe)) visit(byId.get(e.pipe)!);
		state.set(j.id, 2);
		order.push(j);
	};
	jobs.forEach(visit);
	const failed: string[] = [];
	for (const job of order) {
		let { from, to } = job;
		let via = job.via ?? [];
		let flip = false;
		// route from a port: a line from a pipe to a port is the same line backwards
		if (!isPort(from) && isPort(to)) {
			[from, to] = [to, from];
			via = via.slice().reverse();
			flip = true;
		}
		let pts: Pt[] | null = null;
		if (isPort(from)) {
			const s = escape(from, obstacles);
			const sd = from.dir ? DIR_INDEX[from.dir] : 4;
			let routed: Pt[] | null = null;
			let tail: Pt[] = [];
			if (isPort(to)) {
				const e = escape(to, obstacles);
				// arriving at the stub, the line runs into the port: against the way it faces
				const ed = to.dir ? OPPOSITE[DIR_INDEX[to.dir]] : 4;
				routed = routeOne(W, H, obstacles, drawn, s, sd, e, ed, -1, via);
				tail = [e, [to.x, to.y]];
			} else {
				const t = index.get(to.pipe);
				if (t !== undefined) routed = routeOne(W, H, obstacles, drawn, s, sd, null, 4, t, via);
			}
			if (routed) pts = simplify([[from.x, from.y], s, ...routed, ...tail]);
			else {
				failed.push(job.id);
				const e: Pt | undefined = isPort(to) ? [to.x, to.y] : nearestOn(drawn[index.get(to.pipe) ?? -1], s);
				pts = e ? simplify([[from.x, from.y], s, [s[0], e[1]], e]) : [[from.x, from.y], s];
			}
		} else {
			// pipe to pipe: not routed; a straight join between their nearest points
			failed.push(job.id);
			const a = drawn[index.get(from.pipe) ?? -1],
				b = isPort(to) ? undefined : drawn[index.get(to.pipe) ?? -1];
			pts = a && b ? [a[0], b[0]] : [];
		}
		if (flip) pts = pts.slice().reverse();
		paths.set(job.id, pts);
		index.set(job.id, drawn.length);
		drawn.push(pts);
	}
	return { paths, failed };
}

/** The point on a polyline nearest p. */
function nearestOn(pts: Pt[] | undefined, p: Pt): Pt | undefined {
	if (!pts || !pts.length) return undefined;
	let best: Pt = pts[0],
		bd = Infinity;
	for (let i = 1; i < pts.length; i++) {
		const [ax, ay] = pts[i - 1],
			[bx, by] = pts[i];
		const L = (bx - ax) ** 2 + (by - ay) ** 2;
		const t = L ? Math.max(0, Math.min(1, ((p[0] - ax) * (bx - ax) + (p[1] - ay) * (by - ay)) / L)) : 0;
		const q: Pt = [ax + t * (bx - ax), ay + t * (by - ay)];
		const d = (q[0] - p[0]) ** 2 + (q[1] - p[1]) ** 2;
		if (d < bd) {
			bd = d;
			best = q;
		}
	}
	return best;
}
