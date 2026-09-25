// Route suggestion: autorouting as a GENERATOR of ordinary interior
// vertices, never live state — see the module doc below suggestRoute().
// Editor-only concern (this is about producing a nice DEFAULT set of
// [x, y] points for a freshly-drawn port-to-port pipe, or a "re-route"
// button's replacement points), so it lives here rather than in the hmi
// runtime package: once generated, the vertices are ordinary doc data, no
// different from anything a user dragged into place by hand.
// Imports the "/mimic" subpath (not the package root) — same reason
// routing.test.ts does: so plain `node --test` (no bundler) doesn't also
// have to load the root's Svelte component re-exports.
import { PORT_STUB, type PortDir } from '@joyautomation/nautilus-hmi/mimic';

export interface RoutePoint {
	x: number;
	y: number;
}

/** A keep-out rectangle — an equipment box already expanded by whatever
 * margin the caller wants routes to clear it by (this module doesn't add
 * its own; see EditorCanvas.svelte's obstaclesFor()). */
export interface ObstacleRect {
	x: number;
	y: number;
	w: number;
	h: number;
}

/** Mirror of hmi/src/lib/mimic.ts's (module-private) DIR_VECTOR — the unit
 * vector a port's effective exit direction points along. */
const DIR_VECTOR: Record<PortDir, [number, number]> = {
	left: [-1, 0],
	right: [1, 0],
	up: [0, -1],
	down: [0, 1]
};

/** The mandatory first (or last) leg off a directional port — same length
 * and direction withDirStub() (hmi/src/lib/mimic.ts) draws at render time.
 * Computed here too so the SUGGESTED route already continues naturally
 * from it (its own first/last vertex lands exactly on the stub position),
 * rather than routing to the bare port point and letting withDirStub()
 * insert an unplanned extra corner right at the anchor. */
function stubPoint(p: RoutePoint, dir: PortDir): RoutePoint {
	const [dx, dy] = DIR_VECTOR[dir];
	return { x: p.x + dx * PORT_STUB, y: p.y + dy * PORT_STUB };
}

const EPS = 1e-6;

/** True when the axis-aligned segment a->b (horizontal or vertical; a
 * diagonal input is treated as never clear, since this module only ever
 * builds orthogonal segments) does not pass through the STRICT interior of
 * `rect` — touching an edge is fine (the caller's margin already provides
 * the buffer; see ObstacleRect). */
function segClearOfRect(a: RoutePoint, b: RoutePoint, rect: ObstacleRect): boolean {
	const rx0 = rect.x, rx1 = rect.x + rect.w;
	const ry0 = rect.y, ry1 = rect.y + rect.h;
	if (Math.abs(a.y - b.y) < EPS) {
		// Horizontal segment at y = a.y.
		if (a.y <= ry0 + EPS || a.y >= ry1 - EPS) return true;
		const x0 = Math.min(a.x, b.x), x1 = Math.max(a.x, b.x);
		return x1 <= rx0 + EPS || x0 >= rx1 - EPS;
	}
	if (Math.abs(a.x - b.x) < EPS) {
		// Vertical segment at x = a.x.
		if (a.x <= rx0 + EPS || a.x >= rx1 - EPS) return true;
		const y0 = Math.min(a.y, b.y), y1 = Math.max(a.y, b.y);
		return y1 <= ry0 + EPS || y0 >= ry1 - EPS;
	}
	return false;
}

function segClear(a: RoutePoint, b: RoutePoint, obstacles: ObstacleRect[]): boolean {
	return obstacles.every((r) => segClearOfRect(a, b, r));
}

function pathClear(points: RoutePoint[], obstacles: ObstacleRect[]): boolean {
	for (let i = 0; i < points.length - 1; i++) {
		if (!segClear(points[i], points[i + 1], obstacles)) return false;
	}
	return true;
}

/** The canonical shapes, cheapest (fewest corners) first: straight (0, only
 * when already axis-aligned), then the two single-corner Ls (dominant axis
 * first, matching orthogonalPoints'/the freehand draw snap's convention),
 * then the two two-corner Zs (split down the middle of whichever axis), for
 * a candidate corner-only vertex list between A and B. */
function canonicalShapes(a: RoutePoint, b: RoutePoint): RoutePoint[][] {
	const shapes: RoutePoint[][] = [];
	if (Math.abs(a.x - b.x) < EPS || Math.abs(a.y - b.y) < EPS) shapes.push([]);
	const horizFirst = { x: b.x, y: a.y };
	const vertFirst = { x: a.x, y: b.y };
	const dominant = Math.abs(b.x - a.x) >= Math.abs(b.y - a.y) ? horizFirst : vertFirst;
	const other = dominant === horizFirst ? vertFirst : horizFirst;
	shapes.push([dominant], [other]);
	const midX = (a.x + b.x) / 2;
	const midY = (a.y + b.y) / 2;
	shapes.push([{ x: midX, y: a.y }, { x: midX, y: b.y }]);
	shapes.push([{ x: a.x, y: midY }, { x: b.x, y: midY }]);
	return shapes;
}

/** "Go around" shapes: hug one of an obstacle's four sides — two corners,
 * routing along the obstacle's own edge rather than through its middle.
 * Tried per-obstacle (in the given order) after the plain canonical shapes
 * fail, before resorting to the grid search. */
function aroundShapes(a: RoutePoint, b: RoutePoint, obstacles: ObstacleRect[]): RoutePoint[][] {
	const shapes: RoutePoint[][] = [];
	for (const r of obstacles) {
		shapes.push([{ x: r.x, y: a.y }, { x: r.x, y: b.y }]);
		shapes.push([{ x: r.x + r.w, y: a.y }, { x: r.x + r.w, y: b.y }]);
		shapes.push([{ x: a.x, y: r.y }, { x: b.x, y: r.y }]);
		shapes.push([{ x: a.x, y: r.y + r.h }, { x: b.x, y: r.y + r.h }]);
	}
	return shapes;
}

/** Coarse-grid fallback: a "visibility graph" of the x/y lines that
 * actually matter (A, B, and every obstacle's four edges) rather than a
 * fixed-pixel grid — precise (routes land exactly on A/B/obstacle edges,
 * no snapping error) and small (a handful of equipment boxes -> a handful
 * of rails), which is what makes this tractable as a synchronous, plain
 * function with no external deps. 0-1 BFS (a direction change costs 1, a
 * straight continuation costs 0) finds the path with the FEWEST corners,
 * not merely the fewest grid hops, matching "minimizing corners". Returns
 * null when no obstacle-clear grid path exists at all. */
function gridRoute(a: RoutePoint, b: RoutePoint, obstacles: ObstacleRect[]): RoutePoint[] | null {
	const xsSet = new Set<number>([a.x, b.x]);
	const ysSet = new Set<number>([a.y, b.y]);
	for (const r of obstacles) {
		xsSet.add(r.x);
		xsSet.add(r.x + r.w);
		ysSet.add(r.y);
		ysSet.add(r.y + r.h);
	}
	const xs = [...xsSet].sort((p, q) => p - q);
	const ys = [...ysSet].sort((p, q) => p - q);
	const xi = new Map(xs.map((x, i) => [x, i]));
	const yi = new Map(ys.map((y, i) => [y, i]));
	const W = xs.length, H = ys.length;
	const idx = (ix: number, iy: number) => iy * W + ix;

	type Dir = 0 | 1 | 2 | 3; // right, left, down, up
	const DX = [1, -1, 0, 0];
	const DY = [0, 0, 1, -1];

	// dist[state] where state = idx(ix,iy) * 5 + (dir+1) (dir=-1 -> 0 = "no
	// incoming direction yet", used only for the start node).
	const NONE = 4;
	const stateCount = W * H * 5;
	const dist = new Array<number>(stateCount).fill(Infinity);
	const prev = new Array<number>(stateCount).fill(-1);
	const deque: number[] = [];

	const startIx = xi.get(a.x)!, startIy = yi.get(a.y)!;
	const goalIx = xi.get(b.x)!, goalIy = yi.get(b.y)!;
	const startState = idx(startIx, startIy) * 5 + NONE;
	dist[startState] = 0;
	deque.push(startState);

	while (deque.length) {
		const state = deque.shift()!;
		const d = dist[state];
		const cellState = Math.floor(state / 5);
		const ix = cellState % W;
		const iy = Math.floor(cellState / W);
		const curDir = state % 5;
		for (let dir = 0; dir < 4; dir++) {
			const nix = ix + DX[dir];
			const niy = iy + DY[dir];
			if (nix < 0 || nix >= W || niy < 0 || niy >= H) continue;
			const from: RoutePoint = { x: xs[ix], y: ys[iy] };
			const to: RoutePoint = { x: xs[nix], y: ys[niy] };
			if (!segClear(from, to, obstacles)) continue;
			const turn = curDir !== NONE && curDir !== dir ? 1 : 0;
			const nd = d + turn;
			const nstate = idx(nix, niy) * 5 + dir;
			if (nd < dist[nstate]) {
				dist[nstate] = nd;
				prev[nstate] = state;
				if (turn === 0) deque.unshift(nstate);
				else deque.push(nstate);
			}
		}
	}

	let best = -1;
	let bestDist = Infinity;
	for (let dir = 0; dir < 5; dir++) {
		const s = idx(goalIx, goalIy) * 5 + dir;
		if (dist[s] < bestDist) {
			bestDist = dist[s];
			best = s;
		}
	}
	if (best === -1 || !isFinite(bestDist)) return null;

	const cellPath: RoutePoint[] = [];
	let cur = best;
	while (cur !== -1) {
		const cellState = Math.floor(cur / 5);
		cellPath.push({ x: xs[cellState % W], y: ys[Math.floor(cellState / W)] });
		cur = prev[cur];
	}
	cellPath.reverse();

	// Collapse collinear runs down to just the turn points (drop A/B — the
	// caller re-adds those; this returns interior corners only).
	const corners: RoutePoint[] = [];
	for (let i = 1; i < cellPath.length - 1; i++) {
		const p0 = cellPath[i - 1], p1 = cellPath[i], p2 = cellPath[i + 1];
		const sameLine = (p1.x === p0.x && p1.x === p2.x) || (p1.y === p0.y && p1.y === p2.y);
		if (!sameLine) corners.push(p1);
	}
	return corners;
}

/** The face of `box` a point sits nearest to, as the direction leaving it
 * — the exit direction a port with no `dir` of its own (and none
 * inferPortDir() can read off an exact box edge) is given for routing. Ties
 * go horizontal first (left, right, up, down). */
export function faceDir(p: RoutePoint, box: ObstacleRect): PortDir {
	const d: [PortDir, number][] = [
		['left', p.x - box.x],
		['right', box.x + box.w - p.x],
		['up', p.y - box.y],
		['down', box.y + box.h - p.y]
	];
	let best = d[0];
	for (const c of d) if (c[1] < best[1] - EPS) best = c;
	return best[0];
}

/** Where a route leaving `p` in `dir` gets clear of its own equipment's
 * keep-out `box`: straight out of the port's face to the box's edge (never
 * shorter than the PORT_STUB leg every directional port draws). */
function exitPoint(p: RoutePoint, dir: PortDir, box: ObstacleRect): RoutePoint {
	const stub = stubPoint(p, dir);
	switch (dir) {
		case 'left':
			return { x: Math.min(stub.x, box.x), y: p.y };
		case 'right':
			return { x: Math.max(stub.x, box.x + box.w), y: p.y };
		case 'up':
			return { x: p.x, y: Math.min(stub.y, box.y) };
		case 'down':
			return { x: p.x, y: Math.max(stub.y, box.y + box.h) };
	}
}

/** The keep-out boxes of the equipment a route STARTS and ENDS on (each
 * already in `obstacles` too). Given, a route first leaves that port
 * outward — its `dir`, or the face it sits on (faceDir) — to the box's
 * edge, and only then routes around everything, its own equipment
 * included; so a pipe off a tank's side port goes around the tank, never
 * back through its body. */
export interface RouteEnds {
	start?: ObstacleRect;
	end?: ObstacleRect;
}

/** Suggest an orthogonal route's INTERIOR vertices between two anchored
 * port positions — a pure GENERATOR, called once (the port-to-port draw
 * gesture, or the "Re-route" button), never re-derived live: the result is
 * ordinary [x, y] points a user then edits like any other pipe vertex.
 *
 * With `ends` (see RouteEnds), each boxed end first exits its own
 * equipment; that leg (port -> the box edge) is the one segment not
 * checked against the obstacles, since it necessarily starts inside its own
 * box. Without a box for an end, the route starts at the port's PORT_STUB
 * point (or the port itself when it has no direction) and the caller is
 * expected to have left that end's equipment out of `obstacles`.
 *
 * Tries, in order (cheapest/simplest first): the straight/L/Z canonical
 * shapes, then hugging each obstacle's four edges, then a coarse
 * visibility-graph search (gridRoute) minimizing corners, and finally —
 * only if literally nothing else clears — the plain L regardless of
 * collision, so this NEVER throws or returns an unusable empty route; it
 * degrades to "at least a shape", same as a user's own freehand fallback
 * would. Deterministic: no randomness anywhere, so re-suggesting from the
 * same inputs always reproduces the same route. */
export function suggestRoute(
	start: RoutePoint,
	startDir: PortDir | undefined,
	end: RoutePoint,
	endDir: PortDir | undefined,
	obstacles: ObstacleRect[],
	ends: RouteEnds = {}
): [number, number][] {
	const sDir = startDir ?? (ends.start ? faceDir(start, ends.start) : undefined);
	const eDir = endDir ?? (ends.end ? faceDir(end, ends.end) : undefined);
	const a = sDir ? (ends.start ? exitPoint(start, sDir, ends.start) : stubPoint(start, sDir)) : start;
	const b = eDir ? (ends.end ? exitPoint(end, eDir, ends.end) : stubPoint(end, eDir)) : end;

	let corners: RoutePoint[] | null = null;
	for (const shape of canonicalShapes(a, b)) {
		if (pathClear([a, ...shape, b], obstacles)) {
			corners = shape;
			break;
		}
	}
	if (!corners) {
		for (const shape of aroundShapes(a, b, obstacles)) {
			if (pathClear([a, ...shape, b], obstacles)) {
				corners = shape;
				break;
			}
		}
	}
	if (!corners) corners = gridRoute(a, b, obstacles);
	if (!corners) corners = [{ x: b.x, y: a.y }]; // plain L, last resort — see doc comment.

	const out: RoutePoint[] = [];
	if (sDir) out.push(a);
	out.push(...corners);
	if (eDir) out.push(b);
	return out.map((p) => [p.x, p.y]);
}
