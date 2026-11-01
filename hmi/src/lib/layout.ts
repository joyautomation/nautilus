// Laying a component's drawing out in its box, so a resized piece of
// equipment reflows instead of stretching: the vessel grows, the level
// scale and the label do not. The idea is CSS grid's — fixed tracks plus
// `fr` tracks that share what is left — done in SVG units, because a mimic
// component is one <svg> whose parts pipes and ports must land on exactly.

/** One track: a fixed size in drawing units, or a share (`'1fr'`, `'2fr'`)
 * of the space the fixed tracks leave. */
export type Track = number | `${number}fr`;

/** A track's place along its axis. */
export interface Span {
	start: number;
	size: number;
	end: number;
}

/** Split `total` into `spec`'s tracks, in order. Fixed tracks get their size;
 * `fr` tracks share the rest by weight (never below zero, so a box too small
 * for its fixed tracks squeezes the flexible ones to nothing, not negative). */
export function tracks(total: number, spec: Track[]): Span[] {
	let fixed = 0;
	let frs = 0;
	for (const t of spec) {
		if (typeof t === 'number') fixed += t;
		else frs += parseFloat(t);
	}
	const free = Math.max(0, total - fixed);
	let at = 0;
	return spec.map((t) => {
		const size = typeof t === 'number' ? t : frs ? (free * parseFloat(t)) / frs : 0;
		const span = { start: at, size, end: at + size };
		at += size;
		return span;
	});
}

/** The drawing's size and how much its fixed parts (text, actuators,
 * scales) scale by. A component designed at `natural` [w, h] draws at
 * `width` x `height`; `height` omitted keeps the natural proportions. At
 * natural proportions `k` is plain `width / natural width`, exactly the
 * uniform scaling a fixed viewBox gave; stretched one way, `k` follows the
 * SHORTER side, so the text stays readable and the body takes the slack. */
export function fitBox(
	width: number,
	height: number | undefined,
	natural: [number, number]
): { w: number; h: number; k: number } {
	const w = Math.max(1, width);
	const h = Math.max(1, height ?? (w * natural[1]) / natural[0]);
	return { w, h, k: Math.min(w / natural[0], h / natural[1]) };
}
