// Function Block Diagram → display list, on the FBD view's own banded
// layout (layout.ts): the same columns, rows, networks and feedback lanes
// the editor draws, without the editor's handles, toolbar, selection or
// live pills. Each network (or, without NETWORK lines, each connected band
// of logic and the comments above it) is one Block: pagination breaks
// between networks, never through one.

import { layout, NET_HEAD_H, normalizeFbd, pinOffset, type FbdEdge, type FbdModel, type Placed } from '../layout.ts';
import { FAINT, INK, MUTED, Pen, SHADE, textWidth, mergeBlocks, type Block, type Diagram, type Seg } from './draw.ts';
import type { LiveSnapshot } from './live.ts';

const PAD = 24;
const NOTE_LINE_H = 15;

type Rect = { x: number; y: number; w: number; h: number };

export function fbdDiagram(model: FbdModel, live?: LiveSnapshot): Diagram {
	const m = normalizeFbd(model);
	// Ghosts exist only on an editing canvas (a reference not yet wired into
	// the text); a printout shows the program.
	const { placed: all, edges: allEdges, laneIdx, frames } = layout(m);
	const placed = all.filter((n) => !n.ghost);
	const byId = new Map(placed.map((n) => [n.id, n]));
	const edges = allEdges.filter((e) => byId.has(e.from) && byId.has(e.to));
	const pen = new Pen();
	const spans: Block[] = [];

	for (const f of frames) {
		pen.rect(f.x, f.y, f.w, f.h, { rx: 4, s: { color: FAINT, width: 0.8 } });
		pen.rect(f.x, f.y, f.w, NET_HEAD_H, { rx: 4, fill: SHADE });
		pen.text(f.x + 10, f.y + 17, `Network ${f.number}`, { size: 11, bold: true });
		if (f.title) pen.text(f.x + 10 + textWidth(`Network ${f.number}   `, 11), f.y + 17, f.title, { size: 11, italic: true, color: MUTED });
		spans.push({ y0: f.y, y1: f.y + f.h });
	}

	const rects: Rect[] = placed.map((n) => ({ x: n.x, y: n.y, w: n.w, h: n.h }));
	const sourceHasWire = new Map(placed.map((n) => [n.id, !!n.wire]));
	for (const e of edges) {
		const from = byId.get(e.from)!;
		const to = byId.get(e.to)!;
		const ys = drawEdge(pen, e, from, to, laneIdx.get(e), rects, !!e.wire && !sourceHasWire.get(e.from));
		spans.push({ y0: ys[0], y1: ys[1] });
	}

	for (const n of placed) {
		drawNode(pen, n, live);
		// A comment holds with the logic below it — never last on a page.
		spans.push({ y0: n.y - 10, y1: n.y + n.h + (n.kind === 'input' || n.kind === 'coil' ? 18 : 10), keep: n.kind === 'comment' });
	}

	let width = 340;
	let height = 0;
	for (const n of placed) {
		width = Math.max(width, n.x + n.w + PAD + (n.wire ? textWidth(n.wire, 9) + 10 : 0));
		height = Math.max(height, n.y + n.h + PAD);
	}
	for (const f of frames) {
		width = Math.max(width, f.x + f.w + PAD);
		height = Math.max(height, f.y + f.h + PAD);
	}
	for (const s of spans) height = Math.max(height, s.y1 + 8);

	return {
		title: m.name || 'fbd',
		kind: 'Function Block Diagram',
		width,
		height,
		items: pen.items,
		blocks: mergeBlocks(spans, 4)
	};
}

/** The editor's wire: a bezier for a forward run, an orthogonal lane for a
 * backward (feedback) one, which drops below whatever it would cross.
 * Returns the wire's vertical extent. */
function drawEdge(
	pen: Pen,
	e: FbdEdge,
	from: Placed,
	to: Placed,
	lane: number | undefined,
	rects: Rect[],
	label: boolean
): [number, number] {
	const sx = from.x + from.w;
	const sy = from.y + pinOffset(from, e.fromPin ?? '', 'out');
	const tx = to.x;
	const ty = to.y + pinOffset(to, e.toPin ?? '', 'in');
	const endX = e.negated ? tx - 9 : tx;
	const backward = sx >= tx - 4;
	let d: Seg[];
	let lo = Math.min(sy, ty);
	let hi = Math.max(sy, ty);
	if (!backward) {
		// @xyflow's bezier for a right-facing source and left-facing target.
		const off = 0.5 * (endX - sx);
		d = [['M', sx, sy], ['C', sx + off, sy, endX - off, ty, endX, ty]];
	} else {
		const k = lane ?? 0;
		const ox = sx + 14 + k * 6;
		const ix = endX - 14 - k * 6;
		const l = Math.min(ix, ox);
		const r = Math.max(ix, ox);
		let ly = Math.max(sy, ty) + 18 + k * 10;
		for (let guard = 0; guard < 50; guard++) {
			let pushed = false;
			for (const b of rects) {
				if (b.x < r && b.x + b.w > l && ly >= b.y - 6 && ly <= b.y + b.h + 6) {
					ly = b.y + b.h + 10 + k * 6;
					pushed = true;
				}
			}
			if (!pushed) break;
		}
		d = [['M', sx, sy], ['L', ox, sy], ['L', ox, ly], ['L', ix, ly], ['L', ix, ty], ['L', endX, ty]];
		hi = Math.max(hi, ly);
	}
	const dash = e.feedback || backward ? [6, 3] : undefined;
	pen.path(d, { s: { color: INK, width: 1.3, dash } });
	if (e.negated) pen.circle(tx - 4.5, ty, 4.5, { fill: '#ffffff', s: { color: INK, width: 1.2 } });
	if (label && e.wire && !backward) pen.text(sx + 6, sy - 5, e.wire, { size: 9, color: MUTED });
	return [lo, hi];
}

function drawNode(pen: Pen, n: Placed, live?: LiveSnapshot): void {
	if (n.kind === 'comment') {
		pen.rect(n.x, n.y, n.w, n.h, { rx: 4, s: { color: FAINT, width: 0.8, dash: [3, 2] } });
		n.label.split('\n').forEach((l, i) => pen.text(n.x + 9, n.y + 4 + (i + 1) * NOTE_LINE_H - 4, l, { size: 11, italic: true, color: MUTED }));
		return;
	}
	if (n.kind === 'input' || n.kind === 'coil') {
		pen.rect(n.x, n.y, n.w, n.h, { rx: n.h / 2, fill: '#ffffff', s: { color: INK, width: 1.2 } });
		pen.text(n.x + n.w / 2, n.y + n.h / 2 + 4, n.label, { size: 12, anchor: 'middle' });
		exec(pen, n);
		if (live) {
			const v = live.resolve(n.label);
			if (v !== undefined) pen.text(n.x + n.w / 2, n.y + n.h + 11, live.format(v, n.label), { size: 9, anchor: 'middle', italic: true, color: MUTED });
		}
		return;
	}
	pen.rect(n.x, n.y, n.w, n.h, { rx: 2, fill: '#ffffff', s: { color: INK, width: 1.2 } });
	pen.line(n.x, n.y + n.titleH, n.x + n.w, n.y + n.titleH, { color: FAINT, width: 0.6 });
	if (n.kind === 'fb') {
		pen.text(n.x + n.w / 2, n.y + 14, n.label, { size: 12, anchor: 'middle', bold: true });
		pen.text(n.x + n.w / 2, n.y + 27, n.type ?? '?', { size: 10, anchor: 'middle', color: MUTED });
	} else {
		pen.text(n.x + n.w / 2, n.y + n.titleH / 2 + 4, n.label, { size: 12, anchor: 'middle', bold: true });
	}
	for (const pin of n.ins) {
		pen.text(n.x + 4, n.y + pinOffset(n, pin, 'in') + 3, pin, { size: 9, bold: pin === 'EN' });
	}
	for (const pin of n.outs) {
		pen.text(n.x + n.w - 4, n.y + pinOffset(n, pin, 'out') + 3, pin, { size: 9, anchor: 'end', bold: pin === 'ENO' });
	}
	if (n.wire) pen.text(n.x + n.w + 6, n.y + 16, n.wire, { size: 9, color: MUTED });
	exec(pen, n);
}

/** A statement's execution order within its network — part of the
 * program's meaning, so it prints. */
function exec(pen: Pen, n: Placed): void {
	if (!n.exec) return;
	const label = String(n.exec);
	const w = Math.max(14, textWidth(label, 9) + 6);
	pen.rect(n.x - 8, n.y - 8, w, 14, { rx: 7, fill: '#ffffff', s: { color: FAINT, width: 0.8 } });
	pen.text(n.x - 8 + w / 2, n.y + 2.5, label, { size: 9, anchor: 'middle', color: MUTED });
}
