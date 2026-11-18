// The resting camera: a three-quarter front view from a fixed direction,
// pulled back exactly far enough that the model's boxes fill a canvas of
// the given aspect. Recomputed when the canvas changes shape, so the same
// page frames the controller in a wide, a square and a tall window.
export type Vec3 = [number, number, number];

export interface Box3 {
	min: Vec3;
	max: Vec3;
}

export interface Rest {
	/** Degrees round the vertical axis from straight on; positive looks from the right. */
	yaw: number;
	/** Degrees above the horizon. */
	pitch: number;
	/** Vertical field of view, degrees. */
	fov: number;
	/** Share of the canvas the box may fill, 0..1. */
	fill: number;
}

export function fitCamera(boxes: Box3[], aspect: number, rest: Rest): { pos: Vec3; target: Vec3; fov: number } {
	const rad = Math.PI / 180;
	const cy = Math.cos(rest.yaw * rad);
	const sy = Math.sin(rest.yaw * rad);
	const cp = Math.cos(rest.pitch * rad);
	const sp = Math.sin(rest.pitch * rad);
	// From the target to the camera, the screen's right, and the screen's up.
	const d: Vec3 = [sy * cp, sp, cy * cp];
	const r: Vec3 = [cy, 0, -sy];
	const u: Vec3 = [-sy * sp, cp, -cy * sp];
	const tanV = Math.tan((rest.fov * rad) / 2);
	const tanH = tanV * aspect;
	const dot = (a: Vec3, b: Vec3) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
	const pts: Vec3[] = [];
	for (const b of boxes)
		for (const x of [b.min[0], b.max[0]]) for (const y of [b.min[1], b.max[1]]) for (const z of [b.min[2], b.max[2]]) pts.push([x, y, z]);
	const mid = (i: 0 | 1 | 2) => (Math.min(...pts.map((p) => p[i])) + Math.max(...pts.map((p) => p[i]))) / 2;
	const target: Vec3 = [mid(0), mid(1), mid(2)];
	let dist = 1;
	// A near corner spreads further across the screen than a far one, so the
	// box's middle is not the picture's middle: pull back until everything
	// fits, slide the target to centre what is seen, and repeat; it settles
	// in a few rounds.
	for (let round = 0; round < 6; round++) {
		let need = 0;
		for (const p of pts) {
			const c: Vec3 = [p[0] - target[0], p[1] - target[1], p[2] - target[2]];
			const toward = dot(c, d);
			need = Math.max(need, toward + Math.abs(dot(c, r)) / (tanH * rest.fill), toward + Math.abs(dot(c, u)) / (tanV * rest.fill));
		}
		dist = need;
		let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
		for (const p of pts) {
			const c: Vec3 = [p[0] - target[0], p[1] - target[1], p[2] - target[2]];
			const depth = dist - dot(c, d);
			const x = dot(c, r) / depth;
			const y = dot(c, u) / depth;
			x0 = Math.min(x0, x);
			x1 = Math.max(x1, x);
			y0 = Math.min(y0, y);
			y1 = Math.max(y1, y);
		}
		const sx = ((x0 + x1) / 2) * dist;
		const sy2 = ((y0 + y1) / 2) * dist;
		for (const i of [0, 1, 2] as const) target[i] += r[i] * sx + u[i] * sy2;
	}
	return { pos: [target[0] + d[0] * dist, target[1] + d[1] * dist, target[2] + d[2] * dist], target, fov: rest.fov };
}
