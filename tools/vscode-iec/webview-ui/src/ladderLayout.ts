// Ladder layout pass, ported from the tentacle-plc ladder editor's
// RSLogix-style auto-layout: a pure function from the annotated rung model
// to absolute SVG geometry — nodes, wires, branch rails, and edit hotspots
// — so the renderer never does geometry inline. Layout is bottom-up: each
// element reports its box and its ascent/descent around the rung wire;
// Series flows left→right taking the max extents, Parallel stacks legs
// downward from the centerline with explicit rails, and short legs are
// right-padded with wires so every leg meets the branch's right rail.
//
// Every node carries its model PATH (and coils their index), so gestures
// address `naut ld edit` ops directly from the geometry.

import type { Ann } from './ladder';

export const L = {
	RAIL_X: 10, // the vertical power rails
	RAIL_LEFT: 30, // where logic starts (wire from the rail feeds it)
	RAIL_RIGHT_MARGIN: 30,
	CONTACT_W: 72,
	CONTACT_H: 24,
	COIL_W: 72,
	COIL_H: 24,
	FN_H: 22,
	FN_PAD: 10,
	FN_MIN_W: 72,
	FB_MIN_W: 104,
	FB_H: 34,
	FB_ARGS_H: 13,
	WIRE_GAP: 10,
	BRANCH_OUTER_GAP: 18, // wider gap flanking a branch's rails
	BRANCH_GAP: 12,
	EMPTY_LEG_W: 36,
	LABEL_TOP: 6, // room above the symbol
	LABEL_BOT: 30, // room below: operand at +12, live value at +24
	LABEL_WAS: 34, // extra room below the box for a diff "was …"/"removed" label
	RUNG_PAD_Y: 14,
	MIN_WIDTH: 560,
} as const;

export type LNode = {
	kind: 'contact' | 'coil' | 'fn' | 'fb';
	x: number;
	y: number;
	w: number;
	h: number;
	ann: Ann;
	path?: number[]; // element address in the rung tree
	coil?: number; // coil index when this is a coil
};
export type LWire = { x1: number; y1: number; x2: number; y2: number; on?: boolean };
export type LVLine = { x: number; y1: number; y2: number; on?: boolean };
/** An edit hotspot: click to insert a contact into a series, a coil into
 * the coil zone, or a new leg onto a branch. */
export type LSpot = {
	x: number;
	y: number;
	op: 'insert' | 'coil' | 'leg';
	series?: number[];
	index?: number;
	branch?: number[];
};
export type RungLayout = {
	nodes: LNode[];
	wires: LWire[];
	vlines: LVLine[];
	spots: LSpot[];
	width: number;
	height: number;
	wireY: number;
};

type Res = {
	nodes: LNode[];
	wires: LWire[];
	vlines: LVLine[];
	spots: LSpot[];
	width: number;
	ascent: number;
	descent: number;
};

/** Rough monospace text width at a given font size. */
const textW = (s: string, px: number) => s.length * px * 0.62;

export function fnWidth(a: Ann): number {
	return Math.max(L.FN_MIN_W, Math.ceil(textW(`${a.el.fn}(${a.el.args ?? ''})`, 10)) + 2 * L.FN_PAD);
}
export function fbWidth(a: Ann): number {
	return Math.max(
		L.FB_MIN_W,
		Math.ceil(textW(a.el.inst ?? '', 11)) + 28,
		Math.ceil(textW(a.el.type ?? '', 10)) + 28,
		Math.ceil(textW(a.el.args ?? '', 9)) + 20
	);
}
function fbHeight(a: Ann): number {
	return L.FB_H + (a.el.args ? L.FB_ARGS_H : 0);
}

const halfC = L.CONTACT_H / 2;

/** A changed/removed element draws a "was …" (or "removed") label under its
 * box, LadderView-style, at local y = nodeH + 24. Grow descent so that label
 * — and its own line height — never overruns into whatever follows the
 * rung (a comment note, or the next rung). */
function reserveWasLabel(a: Ann, cy: number, nodeY: number, nodeH: number, descent: number): number {
	const showsWas = a.el._diff === 'removed' || (a.el._diff === 'changed' && !!a.el._was);
	if (!showsWas) return descent;
	const need = nodeY - cy + nodeH + L.LABEL_WAS;
	return Math.max(descent, need);
}

/** Lay out one series of condition elements centered on cy, starting at x.
 * path addresses this series in the rung tree ([] = the root). */
export function layoutSeries(anns: Ann[], x: number, cy: number, path: number[] = []): Res {
	const nodes: LNode[] = [];
	const wires: LWire[] = [];
	const vlines: LVLine[] = [];
	const spots: LSpot[] = [];
	let cursor = x;
	let ascent = halfC + L.LABEL_TOP;
	let descent = halfC + L.LABEL_BOT;

	if (anns.length === 0) {
		// An empty leg is a straight, always-true wire with reserved width.
		wires.push({ x1: cursor, y1: cy, x2: cursor + L.EMPTY_LEG_W, y2: cy, on: undefined });
		spots.push({ x: cursor + L.EMPTY_LEG_W / 2, y: cy, op: 'insert', series: path, index: 0 });
		return { nodes, wires, vlines, spots, width: L.EMPTY_LEG_W, ascent, descent };
	}

	anns.forEach((a, i) => {
		if (i > 0) {
			const prev = anns[i - 1];
			const nearBranch = prev.el.kind === 'branch' || a.el.kind === 'branch';
			const gap = nearBranch ? L.BRANCH_OUTER_GAP : L.WIRE_GAP;
			wires.push({ x1: cursor, y1: cy, x2: cursor + gap, y2: cy, on: prev.out });
			spots.push({ x: cursor + gap / 2, y: cy, op: 'insert', series: path, index: i });
			cursor += gap;
		}
		const elPath = [...path, i];
		switch (a.el.kind) {
			case 'contact': {
				const y = cy - halfC;
				nodes.push({ kind: 'contact', x: cursor, y, w: L.CONTACT_W, h: L.CONTACT_H, ann: a, path: elPath });
				descent = reserveWasLabel(a, cy, y, L.CONTACT_H, descent);
				cursor += L.CONTACT_W;
				break;
			}
			case 'fn': {
				const w = fnWidth(a);
				const y = cy - L.FN_H / 2;
				nodes.push({ kind: 'fn', x: cursor, y, w, h: L.FN_H, ann: a, path: elPath });
				descent = reserveWasLabel(a, cy, y, L.FN_H, descent);
				cursor += w;
				break;
			}
			case 'fb': {
				const w = fbWidth(a);
				const h = fbHeight(a);
				// The box hangs mostly below the wire: power crosses near
				// its top so following elements stay on the rung line.
				const top = cy - 12;
				nodes.push({ kind: 'fb', x: cursor, y: top, w, h, ann: a, path: elPath });
				const below = top + h - cy;
				if (below + L.LABEL_BOT - 12 > descent) descent = below + 6;
				descent = reserveWasLabel(a, cy, top, h, descent);
				if (12 + L.LABEL_TOP + 12 > ascent) ascent = 12 + L.LABEL_TOP + 12; // inst label above
				cursor += w;
				break;
			}
			case 'branch': {
				const legs = (a.legs ?? []).map((leg, li) => layoutSeries(leg, 0, 0, [...elPath, li]));
				const maxW = Math.max(...legs.map((l) => l.width), L.EMPTY_LEG_W);
				// First leg rides the centerline; the rest stack downward.
				let pen = cy;
				let prevDescent = 0;
				const legYs: number[] = [];
				(a.legs ?? []).forEach((leg, li) => {
					const probe = legs[li];
					if (li > 0) pen += prevDescent + L.BRANCH_GAP + probe.ascent;
					legYs.push(pen);
					const r = layoutSeries(leg, cursor, pen, [...elPath, li]);
					nodes.push(...r.nodes);
					wires.push(...r.wires);
					vlines.push(...r.vlines);
					spots.push(...r.spots);
					// Right-pad short legs so every leg reaches the right rail,
					// the pad wire carrying the leg's own output power.
					if (r.width < maxW) {
						const legOut = leg.length ? leg[leg.length - 1].out : a.in;
						wires.push({ x1: cursor + r.width, y1: pen, x2: cursor + maxW, y2: pen, on: legOut });
					}
					prevDescent = r.descent;
				});
				// Branch rails: left carries the branch's input power, right
				// its output.
				const top = legYs[0];
				const bottom = legYs[legYs.length - 1];
				vlines.push({ x: cursor, y1: top, y2: bottom, on: a.in });
				vlines.push({ x: cursor + maxW, y1: top, y2: bottom, on: a.out });
				// The add-leg hotspot sits below the branch's last leg labels.
				const lastProbe = legs[legs.length - 1];
				const legSpotY = bottom + lastProbe.descent + 2;
				spots.push({ x: cursor + maxW / 2, y: legSpotY, op: 'leg', branch: elPath });
				// Extents: legs below the centerline deepen the descent.
				const depth = bottom - cy + lastProbe.descent + 14; // room for the leg spot
				if (depth > descent) descent = depth;
				if (legs[0].ascent > ascent) ascent = legs[0].ascent;
				cursor += maxW;
				break;
			}
		}
	});
	// End-of-series insert point (start inserts use index 0 via the first gap
	// or, for the root, the rail feed spot emitted by layoutRung).
	spots.push({ x: cursor + 6, y: cy, op: 'insert', series: path, index: anns.length });
	return { nodes, wires, vlines, spots, width: cursor - x, ascent, descent };
}

/** Measure a rung's natural width (logic + coil zone) for the canvas pass. */
export function rungMinWidth(elems: Ann[], coilCount: number): number {
	const probe = layoutSeries(elems, 0, 0);
	const coilsW = coilCount * L.COIL_W + Math.max(0, coilCount - 1) * L.WIRE_GAP;
	return L.RAIL_LEFT + probe.width + L.WIRE_GAP + coilsW + L.RAIL_RIGHT_MARGIN;
}

/** Lay out one rung at the given canvas width: logic at the left rail,
 * coils right-aligned against the right rail (RSLogix-style), the
 * connector wire stretched between them. */
export function layoutRung(elems: Ann[], coils: Ann[], width: number): RungLayout {
	const probe = layoutSeries(elems, 0, 0);
	const ascent = Math.max(probe.ascent, halfC + L.LABEL_TOP);
	let descent = Math.max(probe.descent, halfC + L.LABEL_BOT);
	const wireY = L.RUNG_PAD_Y + ascent;

	const r = layoutSeries(elems, L.RAIL_LEFT, wireY);
	const nodes = [...r.nodes];
	const wires = [...r.wires];
	const vlines = [...r.vlines];
	const spots = [...r.spots];

	// Rail feed: left rail to the first element, plus the insert-at-start spot.
	wires.push({ x1: L.RAIL_X, y1: wireY, x2: L.RAIL_LEFT, y2: wireY, on: elems.length ? elems[0].in : coils[0]?.in });
	spots.push({ x: (L.RAIL_X + L.RAIL_LEFT) / 2, y: wireY, op: 'insert', series: [], index: 0 });

	const outPower = elems.length ? elems[elems.length - 1].out : (coils[0]?.in ?? undefined);
	const coilsW = coils.length * L.COIL_W + Math.max(0, coils.length - 1) * L.WIRE_GAP;
	const coilsX = Math.max(width - L.RAIL_RIGHT_MARGIN - coilsW, L.RAIL_LEFT + r.width + L.WIRE_GAP);

	// The stretch wire from the logic to the coil zone.
	wires.push({ x1: L.RAIL_LEFT + r.width, y1: wireY, x2: coilsX, y2: wireY, on: outPower });

	let cx = coilsX;
	coils.forEach((c, i) => {
		if (i > 0) {
			wires.push({ x1: cx, y1: wireY, x2: cx + L.WIRE_GAP, y2: wireY, on: outPower });
			cx += L.WIRE_GAP;
		}
		const coilY = wireY - L.COIL_H / 2;
		nodes.push({ kind: 'coil', x: cx, y: coilY, w: L.COIL_W, h: L.COIL_H, ann: c, coil: i });
		descent = reserveWasLabel(c, wireY, coilY, L.COIL_H, descent);
		cx += L.COIL_W;
	});
	// Coil zone to the right rail, with the add-coil spot on it.
	wires.push({ x1: cx, y1: wireY, x2: width - L.RAIL_X, y2: wireY, on: outPower });
	spots.push({ x: (cx + width - L.RAIL_X) / 2, y: wireY, op: 'coil', index: coils.length });

	return { nodes, wires, vlines, spots, width, height: wireY + descent + L.RUNG_PAD_Y / 2, wireY };
}
