// Pure math behind the ports-edit gesture — shared by the mimic editor's
// ports mode (EditorCanvas.svelte, 'p' on a selected equipment instance)
// and the component preview pane (PreviewApp.svelte), so "drag a dot
// to move it" and "double-click the box to add one, snapped to the nearest
// edge/quarter" behave identically in both places instead of being
// reimplemented twice.

export type Box = { x: number; y: number; w: number; h: number };

/** The direction a pipe LEAVES a port. Mirror of hmi/src/lib/mimic.ts's
 * PortDir. */
export type PortDir = 'left' | 'right' | 'up' | 'down';

/** A named connection point, as a fraction of the rendered box (or of the
 * drawing's `part`, when it names one). Mirror of
 * src/mimicComponentIndex.ts's Port + hmi/src/lib/mimic.ts's MimicPort. The
 * name is how a pipe end anchors to it explicitly (a mimic pipe's
 * from/to). `dir` is the direction a pipe leaves the port; absent = inferred
 * from its edge position (hmi's inferPortDir/resolvedPortDir). Names are
 * unique within a component's port list. */
export type Port = { name: string; x: number; y: number; dir?: PortDir; part?: string };

export const clamp01 = (v: number): number => Math.max(0, Math.min(1, v));

/** A raw canvas-pixel point -> its fraction within `box`, clamped to
 * [0, 1] on each axis (dragging past an edge pins the port to it). */
export function toFraction(box: Box, p: [number, number]): [number, number] {
	return [clamp01((p[0] - box.x) / box.w), clamp01((p[1] - box.y) / box.h)];
}

/** The smallest of a drawing's parts (name + canvas-pixel frame) under a
 * point, with `slop` pixels of give so a thin part (a flange, a nozzle)
 * can still be hit — or null over none. */
export function smallestPartAt(parts: [string, Box][], p: [number, number], slop = 4): [string, Box] | null {
	let best: [string, Box] | null = null;
	for (const [name, f] of parts) {
		const inside = p[0] >= f.x - slop && p[0] <= f.x + f.w + slop && p[1] >= f.y - slop && p[1] <= f.y + f.h + slop;
		if (inside && (!best || f.w * f.h < best[1].w * best[1].h)) best = [name, f];
	}
	return best;
}

/** Where a dragged port lands: on the part under the point when there is
 * one (its fractions are then of that part, and `part` names it), else a
 * fraction of the whole box with no part. A drag can carry a port from
 * one part to another, or off the drawing's parts altogether. */
export function dropPort(box: Box, parts: [string, Box][], p: [number, number]): { x: number; y: number; part?: string } {
	const hit = smallestPartAt(parts, p);
	const [x, y] = toFraction(hit ? hit[1] : box, p);
	return hit ? { x, y, part: hit[0] } : { x, y };
}

/** `port` moved to `to` — x, y and part replaced (a part-less landing
 * drops the old part), everything else (name, dir) kept. */
export function movePort(port: Port, to: { x: number; y: number; part?: string }): Port {
	const { part: _old, ...rest } = port;
	return { ...rest, x: to.x, y: to.y, ...(to.part !== undefined ? { part: to.part } : {}) };
}

const QUARTER_GRID = [0, 0.25, 0.5, 0.75, 1];
const snapAxis = (v: number): number => QUARTER_GRID.reduce((a, b) => (Math.abs(b - v) < Math.abs(a - v) ? b : a));

/** The "add a port" gesture: project a fraction-space point onto its
 * nearer edge (the axis closer to 0 or 1 flips to that bound), then snap
 * BOTH axes to the 0/.25/.5/.75/1 grid — simple, and lines up with where
 * ports usually belong. */
export function edgeSnap(fx: number, fy: number): [number, number] {
	const distX = Math.min(fx, 1 - fx);
	const distY = Math.min(fy, 1 - fy);
	let [ox, oy] = [fx, fy];
	if (distX <= distY) ox = fx < 0.5 ? 0 : 1;
	else oy = fy < 0.5 ? 0 : 1;
	return [snapAxis(ox), snapAxis(oy)];
}

/** Arrow-key nudge for a ports-edit gesture: step the port by `dx`/`dy`
 * CANVAS PIXELS (1 per plain arrow press, GRID per Shift-arrow — the same
 * step convention EditorCanvas.svelte's equipment/pipe-end nudges use),
 * converting through the box the same way a pointer drag does (toFraction,
 * clamped to [0, 1] at the edges) so a keyboard nudge and a physical drag
 * settle to identical values. Deliberately mirrors the drag gesture's OWN
 * precision: unrounded here, rounded only on commit (roundPort, in
 * commitPorts) — a dot-drag doesn't round mid-flight either. */
export function nudgePort(box: Box, port: Port, dx: number, dy: number): Port {
	const [x, y] = toFraction(box, [box.x + port.x * box.w + dx, box.y + port.y * box.h + dy]);
	return { ...port, x, y };
}

const round3 = (v: number): number => Math.round(v * 1000) / 1000;

/** Round a port's fractions to 3 decimal places — the precision committed
 * to disk. The name, dir and part (if set) pass through unchanged. */
export function roundPort(port: Port): Port {
	return {
		name: port.name,
		...(port.part !== undefined ? { part: port.part } : {}),
		x: round3(port.x),
		y: round3(port.y),
		...(port.dir !== undefined ? { dir: port.dir } : {})
	};
}

/** "0.5, 0" — the coordinate readout shown near a dragged dot / in a hover
 * tooltip. Takes plain fractions (not a Port) so it works for an in-flight
 * drag too, before it has settled back into the ports list. */
export function fmtFraction(fx: number, fy: number): string {
	return `${round3(fx)}, ${round3(fy)}`;
}

// ── auto-naming + free-slot placement (Add port / double-click-to-add) ────

/** Candidate slots for a graphically-added port, in priority order: each
 * edge's midpoint first, then its two quarter points — the "scan top/right/
 * bottom/left midpoints then quarters" rule for the ports panel's "Add
 * port" button. */
const EDGE_SLOTS: [number, number][] = [
	[0.5, 0], [1, 0.5], [0.5, 1], [0, 0.5], // top, right, bottom, left midpoints
	[0.25, 0], [0.75, 0], // top quarters
	[1, 0.25], [1, 0.75], // right quarters
	[0.75, 1], [0.25, 1], // bottom quarters
	[0, 0.75], [0, 0.25] // left quarters
];

const closeEnough = (a: number, b: number): boolean => Math.abs(a - b) < 0.001;

/** The first free slot from EDGE_SLOTS not already occupied by an existing
 * port — falls back to dead center if every quarter-grid slot on the
 * outline is taken. */
export function firstFreeSlot(existing: Port[]): [number, number] {
	for (const [x, y] of EDGE_SLOTS) {
		if (!existing.some((p) => closeEnough(p.x, x) && closeEnough(p.y, y))) return [x, y];
	}
	return [0.5, 0.5];
}

/** First free `p1`, `p2`, … name not already taken — used for every
 * graphically-added port (double-click-to-add and the ports panel's "Add
 * port" button alike). */
export function nextPortName(existing: Port[]): string {
	const taken = new Set(existing.map((p) => p.name));
	for (let n = 1; ; n++) {
		const name = `p${n}`;
		if (!taken.has(name)) return name;
	}
}

/** Build a new auto-named port at the first free quarter-position slot on
 * the outline — the ports panel's "Add port" button. */
export function newPortAtFreeSlot(existing: Port[]): Port {
	const [x, y] = firstFreeSlot(existing);
	return { name: nextPortName(existing), x, y };
}

/** Build a new auto-named port from a double-click point, projected onto
 * its nearer edge and quarter-snapped (edgeSnap) — the double-click-the-
 * outline-to-add gesture shared by both editors. */
export function newPortAtPoint(box: Box, raw: [number, number], existing: Port[]): Port {
	const [fx, fy] = toFraction(box, raw);
	const [x, y] = edgeSnap(fx, fy);
	return { name: nextPortName(existing), x, y };
}
