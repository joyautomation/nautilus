// Where the parts of the built-in drawings sit, as plain functions of the
// box. Each built-in component (Tank, Valve, Pump) lays itself out with its
// function here, and code without a DOM (attachPipeEnds, a tool writing a
// doc) asks builtinParts() where a part-anchored port lands — the same
// numbers either way. A user component has no entry: its parts are
// measured off the DOM (partRect in mimic.ts).
import { fitBox, tracks, type Span } from './layout.js';
import type { PartRect } from './mimic.js';

export interface TankGeometry {
	w: number;
	h: number;
	k: number;
	/** The shell's outer rectangle — `data-part="vessel"`. */
	vessel: PartRect;
	/** The row under the vessel, where the label goes. */
	labelRow: Span;
	/** Text size: the scale's numbers and the label. */
	font: number;
}

/** Tank: a 12-unit margin, the vessel, then the level scale (44, or wider
 * if its numbers need it — text never drops below 8) on the right; a
 * margin, the vessel, then the label row (32, or a margin) below. */
export function tankGeometry(
	width: number,
	height: number | undefined,
	opts: { label?: string; scale?: boolean } = {}
): TankGeometry {
	const { w, h, k } = fitBox(width, height, [220, 260]);
	const scale = opts.scale ?? true;
	const font = Math.max(8, 12 * k);
	// tick (18k to the text) + "100" at ~1.9 em
	const cols = tracks(w, [12 * k, '1fr', scale ? Math.max(44 * k, 18 * k + 2 * font) : 12 * k]);
	const rows = tracks(h, [10 * k, '1fr', opts.label ? 32 * k : 10 * k]);
	return {
		w,
		h,
		k,
		vessel: { x: cols[1].start, y: rows[1].start, w: cols[1].size, h: rows[1].size },
		labelRow: rows[2],
		font
	};
}

export interface ValveGeometry {
	w: number;
	h: number;
	k: number;
	/** The bow-tie body, the full width of the box — `data-part="body"`. */
	body: PartRect;
	labelRow: Span;
}

/** Valve: the actuator (28) above the body, the label (20) below; the body
 * spans the whole width so the line's ends are the box's edges. */
export function valveGeometry(width: number, height: number | undefined): ValveGeometry {
	const { w, h, k } = fitBox(width, height, [90, 80]);
	const rows = tracks(h, [28 * k, '1fr', 20 * k]);
	return { w, h, k, body: { x: 0, y: rows[1].start, w, h: rows[1].size }, labelRow: rows[2] };
}

export interface PumpGeometry {
	w: number;
	h: number;
	k: number;
	/** Volute centre and radius. */
	cx: number;
	cy: number;
	r: number;
	/** Nozzles out to the box's left and top edges. */
	suction: PartRect;
	discharge: PartRect;
}

/** Pump: a symbol that keeps its shape — the volute scales with the shorter
 * side and sits centred in any extra room; the nozzles reach the edges. */
export function pumpGeometry(width: number, height: number | undefined): PumpGeometry {
	const { w, h, k } = fitBox(width, height, [120, 110]);
	const cx = 58 * k + (w - 120 * k) / 2;
	const cy = 58 * k + (h - 110 * k) / 2;
	const r = 34 * k;
	return {
		w,
		h,
		k,
		cx,
		cy,
		r,
		suction: { x: 0, y: cy - 6 * k, w: cx - r + 4 * k, h: 12 * k },
		discharge: { x: cx - 6 * k, y: 0, w: 12 * k, h: cy - r + 4 * k }
	};
}

/** The named parts of a built-in drawing at a box size, or undefined for a
 * component this module doesn't draw. `eq` supplies what changes the
 * layout besides size (a Tank's label and scale). */
export function builtinParts(
	component: string,
	box: { w: number; h: number },
	eq: { label?: string; props?: Record<string, unknown> } = {}
): Record<string, PartRect> | undefined {
	switch (component) {
		case 'Tank': {
			const label = typeof eq.props?.label === 'string' ? eq.props.label : eq.label;
			const scale = eq.props?.scale === false ? false : undefined;
			return { vessel: tankGeometry(box.w, box.h, { label, scale }).vessel };
		}
		case 'Valve':
			return { body: valveGeometry(box.w, box.h).body };
		case 'Pump': {
			const g = pumpGeometry(box.w, box.h);
			return { suction: g.suction, discharge: g.discharge };
		}
		default:
			return undefined;
	}
}
