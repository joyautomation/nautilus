// Where the bench station's parts are, and every wire on it: which terminal
// each end sits in and the path between. No rendering here, so the same
// numbers draw the screen (Axc.svelte) and can be checked without it
// (checkWiring, run by tools/check-wiring.mjs): no wire may pass through a
// housing, the rail, the board, a foot or the desk, and both ends of each
// must sit in their terminals.
//
// Millimetres, in the leaning board's own frame: x along the rail from the
// controller's left edge, y up from the rail's middle, z out from the rail.
export type Vec3 = [number, number, number];

/** The board, before centring. */
export const BOARD = { x0: -97, x1: 263, y0: -140, y1: 125, z: -14 };
/** How far the board leans back in its feet, radians. */
export const TILT = 0.365;
/** The desk top, and the feet the board stands in, in the upright frame (the board's frame before the lean). */
export const DESK_Y = BOARD.y0 - 6;
export const FEET = { xs: [BOARD.x0 + 34, BOARD.x1 - 26], w: 26, h: 22, len: 172, z: BOARD.z - 58 };

export const AXC = { w: 45, h: 126.9, d: 75 };
/** The lower-left corner of the controller's face steps back; the supply plug sits inside the step. */
export const STEP = { w: 19.5, top: 77.5, depth: 17 };
export const PLUG = { x: 10.4, y: AXC.h / 2 - 110.5, w: 12.4, h: 23, z1: AXC.d - STEP.depth + 13 };
export const ETH = { x: 31, y: -AXC.h / 2 - 1, z: 58, w: 11, h: 12, d: 13 };
export const BP = { x: 45, w: 40, h: 122.4, d: 36.9 };
export const SE = { w: 14.6, h: 60, z0: 20, z1: 54 };
/** A Smart Element's left edge and top, by its place on the local bus (1..4): down the left column, then down the right. */
export const seX = (slot: number) => BP.x + 10 + (slot > 2 ? 15 : 0) + 0.2;
export const seTop = (slot: number) => (slot % 2 === 1 ? 60.6 : -0.6);
export const TERM = { x0: -21, w: 21, h: 58, d: 46 };
export const RAIL = { x0: -69, x1: 137, h: 35, z0: -11.5, z1: -4.2 };
/** Where the wiring goes through the board, each with a grommet. */
export const HOLES = [-12, 8, 52, 72, 86];
export const HOLE_Y = -100;
export const JACK = { x: -46, y: -100 };

// The terminal points, as the faces are printed (Axc.svelte draws them from these).
/** On an element's face, from its top-left corner: point k of a field, alternating left and right. */
export const sePoint = (k: number): [number, number] => [k % 2 === 0 ? 7.4 : 10.6, 6.6 + k * 3 + 1.5];
/** On the terminal block's face, from its top-left corner: column 0..3, row 0..3. */
export const TERM_ROWS = [5, 13.5, 44, 52.5];
export const termPoint = (col: number, row: number): [number, number] => [2.7 + col * 5.2, TERM_ROWS[row]];
/** On the supply plug's face: column 0..1, row 0..2. */
export const plugPoint = (col: number, row: number): [number, number] => [3 + col * 6.2, 6 + row * 7];
/** On the backplane's own strip: its supply points. */
export const BP_POINTS = [78, 85, 92, 99];

/** A terminal: the mouth of its hole, and the way a wire leaves it. */
interface Terminal {
	at: Vec3;
	out: Vec3;
	name: string;
}
const Z: Vec3 = [0, 0, 1];
const element = (slot: number, k: number, name: string): Terminal => {
	const [x, y] = sePoint(k);
	return { at: [seX(slot) + x, seTop(slot) - y, SE.z1], out: Z, name };
};
const term = (col: number, row: number): Terminal => {
	const [x, y] = termPoint(col, row);
	return { at: [TERM.x0 + x, TERM.h / 2 - y, TERM.d], out: Z, name: `terminal block ${col + 1}.${row + 1}` };
};
const plug = (col: number, row: number): Terminal => {
	const [x, y] = plugPoint(col, row);
	return { at: [PLUG.x - PLUG.w / 2 + 0.2 + x, PLUG.y + PLUG.h / 2 - y, PLUG.z1], out: Z, name: `supply plug ${col + 1}.${row + 1}` };
};
const backplane = (i: number): Terminal => ({ at: [BP.x + 5, BP.h / 2 - BP_POINTS[i], BP.d], out: Z, name: `backplane supply ${i + 1}` });
/** Through the board at a grommet, dx off its middle. */
const hole = (i: number, dx: number): Terminal => ({ at: [HOLES[i] + dx, HOLE_Y, BOARD.z + 3], out: Z, name: `grommet ${i + 1}` });

// The lean: between the board's frame and the upright one the desk and the feet are in.
const cos = Math.cos(TILT);
const sin = Math.sin(TILT);
export const toUpright = ([x, y, z]: Vec3): Vec3 => [x, BOARD.y0 + (y - BOARD.y0) * cos + (z - BOARD.z) * sin, BOARD.z - (y - BOARD.y0) * sin + (z - BOARD.z) * cos];
export const fromUpright = ([x, Y, Zu]: Vec3): Vec3 => [x, BOARD.y0 + (Y - BOARD.y0) * cos - (Zu - BOARD.z) * sin, BOARD.z + (Y - BOARD.y0) * sin + (Zu - BOARD.z) * cos];
/** A point lying on the desk (a lead of radius r resting on it), given where it is across and out from the board's foot. */
const onDesk = (x: number, zu: number, r: number, lift = 0): Vec3 => fromUpright([x, DESK_Y + r + 0.4 + lift, zu]);

export interface Wire {
	name: string;
	color: string;
	/** Radius, mm. */
	r: number;
	/** The terminal each end sits in; absent where a lead runs off along the desk. */
	from: Terminal;
	to?: Terminal;
	/** The path's centreline, closely sampled. */
	path: Vec3[];
}

/** A smooth curve through the points (Catmull-Rom), sampled `per` times a span. */
function curve(pts: Vec3[], per = 14): Vec3[] {
	const p = [pts[0], ...pts, pts[pts.length - 1]];
	const out: Vec3[] = [];
	for (let i = 1; i < p.length - 2; i++)
		for (let s = 0; s < per; s++) {
			const t = s / per;
			out.push([0, 1, 2].map((k) => 0.5 * (2 * p[i][k] + (p[i + 1][k] - p[i - 1][k]) * t + (2 * p[i - 1][k] - 5 * p[i][k] + 4 * p[i + 1][k] - p[i + 2][k]) * t * t + (3 * p[i][k] - p[i - 1][k] - 3 * p[i + 1][k] + p[i + 2][k]) * t * t * t)) as Vec3);
		}
	out.push(pts[pts.length - 1]);
	return out;
}
/** How deep a wire's end sits in its terminal, and how far it runs straight out before it bends. */
const SEAT = 3;
const LEAD = 6;
const along = (t: Terminal, d: number): Vec3 => [t.at[0] + t.out[0] * d, t.at[1] + t.out[1] * d, t.at[2] + t.out[2] * d];
/** A wire from one terminal to another by way of `via`: seated in each, straight out of each before it bends. */
function run(name: string, color: string, r: number, from: Terminal, via: Vec3[], to: Terminal): Wire {
	return { name, color, r, from, to, path: curve([along(from, -SEAT), along(from, LEAD * 0.4), along(from, LEAD), ...via, along(to, LEAD), along(to, LEAD * 0.4), along(to, -SEAT)]) };
}

const RED = '#c9302a';
const BLUE = '#2a5fc4';
const GREY = '#9a9ea2';
const FRONT = AXC.d + 9;

export const WIRES: Wire[] = [
	// the terminal block's bottom row, straight down past its foot and through the first grommet
	...[0, 1, 2, 3].map((c) => {
		const t = term(c, 3);
		const h = hole(0, (c - 1.5) * 1.8);
		return run(`terminal block ${c + 1}`, c < 2 ? RED : BLUE, 0.85, t, [[t.at[0], -38, TERM.d + 5], [h.at[0], -62, 34], [h.at[0], -86, 16]], h);
	}),
	// the supply plug in the controller's step: out past the face, down under the housing, to the second grommet
	...[0, 1, 2, 3].map((i) => {
		const t = plug(i % 2, 1 + Math.floor(i / 2));
		const h = hole(1, (i - 1.5) * 1.8);
		return run(`supply plug ${i + 1}`, i % 2 ? BLUE : RED, 0.85, t, [[t.at[0], t.at[1] - 12, FRONT + i], [h.at[0], -72, FRONT - 6 + i], [h.at[0], -90, 40], [h.at[0], -98, 18]], h);
	}),
	// the backplane's supply points, down the gap between the controller and the elements, to the third grommet
	...[0, 1].map((i) => {
		const t = backplane(i);
		const h = hole(2, i * 2 - 1);
		return run(`backplane supply ${i + 1}`, i ? BLUE : RED, 0.85, t, [[50, -44, 46 + i * 2], [50, -66, 42], [h.at[0], -86, 22]], h);
	}),
	// the DI16's inputs 01..04 (its first four points), to the buttons: out, down in front of the element, to the fourth grommet
	...[0, 1, 2, 3].map((k) => {
		const t = element(2, k, `DI16 input ${k + 1}`);
		const h = hole(3, (k - 1.5) * 1.7);
		return run(`DI16 input ${k + 1}`, GREY, 0.85, t, [[t.at[0] + 2, -34, SE.z1 + 8 + k], [66 + k, -58, SE.z1 + 7 + k], [h.at[0], -76, 44], [h.at[0], -92, 20]], h);
	}),
	// the AI4 U's first input pair, to the slider: the same way, to the fifth grommet
	...[0, 1].map((k) => {
		const t = element(4, k, `AI4 U input 1 ${k ? '-' : '+'}`);
		const h = hole(4, k * 2 - 1);
		return run(`AI4 U input 1 ${k ? '-' : '+'}`, BLUE, 0.85, t, [[t.at[0] + 3, -32, SE.z1 + 7 + k * 2], [84 + k, -58, SE.z1 + 6], [h.at[0], -78, 40], [h.at[0], -93, 20]], h);
	}),
	// Ethernet: out of the plug under the controller, down onto the desk and away along it
	(() => {
		const r = 2.4;
		const start: Terminal = { at: [ETH.x, ETH.y - ETH.h / 2, ETH.z], out: [0, -1, 0], name: 'Ethernet plug' };
		const u = toUpright(along(start, 30));
		return { name: 'Ethernet', color: '#1d4fb8', r, from: start, path: curve([along(start, -SEAT), along(start, 6), along(start, 16), fromUpright([u[0] - 2, u[1] - 16, u[2] + 22]), onDesk(22, u[2] + 56, r, 7), onDesk(8, u[2] + 84, r, 1.2), onDesk(-40, u[2] + 112, r), onDesk(-150, u[2] + 128, r), onDesk(-290, u[2] + 134, r)]) };
	})(),
	// the supply lead: out of the board's jack, down onto the desk in front of the foot and away along it
	(() => {
		const r = 1.7;
		const start: Terminal = { at: [JACK.x, JACK.y, BOARD.z + 7], out: Z, name: 'supply jack' };
		const u = toUpright(along(start, 22));
		return { name: 'supply lead', color: '#101112', r, from: start, path: curve([along(start, -SEAT), along(start, 8), along(start, 22), fromUpright([u[0] - 3, u[1] - 14, u[2] + 20]), onDesk(JACK.x - 8, u[2] + 42, r, 6), onDesk(JACK.x - 22, u[2] + 62, r, 1), onDesk(-130, u[2] + 82, r), onDesk(-290, u[2] + 88, r)]) };
	})()
];

interface Body {
	name: string;
	/** In the board's frame unless `upright`. */
	min: Vec3;
	max: Vec3;
	upright?: boolean;
}
const body = (name: string, x0: number, x1: number, y0: number, y1: number, z0: number, z1: number, upright = false): Body => ({ name, min: [x0, y0, z0], max: [x1, y1, z1], upright });
const stepY = AXC.h / 2 - STEP.top;
/** Everything solid a wire could run into. */
export const BODIES: Body[] = [
	body('controller, above its step', 0, AXC.w, stepY, AXC.h / 2, 0, AXC.d),
	body('controller, right of its step', STEP.w, AXC.w, -AXC.h / 2, stepY, 0, AXC.d),
	body('controller, behind its step', 0, STEP.w, -AXC.h / 2, stepY, 0, AXC.d - STEP.depth),
	body('supply plug', PLUG.x - PLUG.w / 2, PLUG.x + PLUG.w / 2, PLUG.y - PLUG.h / 2, PLUG.y + PLUG.h / 2, AXC.d - STEP.depth, PLUG.z1),
	body('Ethernet plug', ETH.x - ETH.w / 2, ETH.x + ETH.w / 2, ETH.y - ETH.h / 2, ETH.y + ETH.h / 2, ETH.z - ETH.d / 2, ETH.z + ETH.d / 2),
	body('backplane', BP.x, BP.x + BP.w, -BP.h / 2, BP.h / 2, 0, BP.d),
	...[1, 2, 3, 4].map((slot) => body(`element in slot ${slot}`, seX(slot), seX(slot) + SE.w, seTop(slot) - SE.h, seTop(slot), SE.z0, SE.z1)),
	body('terminal block', TERM.x0, TERM.x0 + TERM.w, -TERM.h / 2, TERM.h / 2, 0, TERM.d),
	body('rail', RAIL.x0, RAIL.x1, -RAIL.h / 2, RAIL.h / 2, RAIL.z0, RAIL.z1),
	body('board', BOARD.x0, BOARD.x1, BOARD.y0, BOARD.y1, BOARD.z, BOARD.z + 3),
	...FEET.xs.map((x, i) => body(`foot ${i + 1}`, x - FEET.w / 2, x + FEET.w / 2, DESK_Y, DESK_Y + FEET.h, FEET.z - FEET.len / 2, FEET.z + FEET.len / 2, true))
];

/** Every way the wiring is wrong; empty when it is right. */
export function checkWiring(): string[] {
	const problems: string[] = [];
	const inside = (p: Vec3, b: Body, grow: number) => [0, 1, 2].every((k) => p[k] > b.min[k] - grow && p[k] < b.max[k] + grow);
	for (const w of WIRES) {
		// the ends: seated SEAT mm inside their terminals, on the terminal's axis
		const ends: [Vec3, Terminal | undefined][] = [[w.path[0], w.from], [w.path[w.path.length - 1], w.to]];
		for (const [p, t] of ends) {
			if (!t) continue;
			const want = along(t, -SEAT);
			const off = Math.hypot(p[0] - want[0], p[1] - want[1], p[2] - want[2]);
			if (off > 0.05) problems.push(`${w.name}: its end is ${off.toFixed(2)} mm from its seat in ${t.name}`);
		}
		// the run between: clear of every body (by the wire's radius) and above the desk. Within reach of a
		// terminal the wire is allowed into what that terminal is part of: that is where it ends.
		let len = 0;
		const dist = w.path.map((p, i) => (i ? (len += Math.hypot(p[0] - w.path[i - 1][0], p[1] - w.path[i - 1][1], p[2] - w.path[i - 1][2])) : 0));
		const reach = SEAT + LEAD + 2;
		const hit = new Map<string, number>();
		w.path.forEach((p, i) => {
			const nearEnd = dist[i] < reach || (w.to !== undefined && len - dist[i] < reach);
			const u = toUpright(p);
			if (u[1] - w.r < DESK_Y - 0.01) hit.set('the desk', Math.max(hit.get('the desk') ?? 0, DESK_Y - (u[1] - w.r)));
			if (nearEnd) return;
			for (const b of BODIES) if (inside(b.upright ? u : p, b, w.r)) hit.set(b.name, (hit.get(b.name) ?? 0) + 1);
		});
		for (const [what, n] of hit) problems.push(what === 'the desk' ? `${w.name}: dips ${n.toFixed(2)} mm into the desk` : `${w.name}: passes through the ${what} (${n} samples)`);
	}
	return problems;
}
