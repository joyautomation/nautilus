// Ladder Diagram → display list. The geometry is the ladder view's own
// (ladderLayout.ts), so the printout is the same drawing the editor shows,
// minus everything that only makes sense on screen: selection, edit
// hotspots, the palette, diagnostics badges, hover tooltips. Rungs stack
// top to bottom with the rails running through them; each rung, comment
// and FUNCTION_BLOCK heading is one Block, so pagination breaks between
// rungs and never through one.

import { annotate, normalizeLd, type Ann, type LdModel, type LdRung } from '../ladder.ts';
import { fitArgs, L, layoutRung, OPERAND_LABEL_MAX, rungMinWidth, type LNode } from '../ladderLayout.ts';
import { FAINT, INK, MONO_ADVANCE, MUTED, Pen, POWER, textWidth, trunc, wrapText, type Block, type Diagram } from './draw.ts';
import type { LiveSnapshot } from './live.ts';

const NOTE_LINE_H = 14;
const POU_H = 30;
const PIN_LINE_H = 12;

/** The narrowest canvas every rung fits in at its natural size. (Rung
 * comments and notes wrap to whatever width the rungs give them.) */
export function ladderNaturalWidth(model: LdModel, live?: LiveSnapshot): number {
	const m = normalizeLd(model);
	return Math.max(
		L.MIN_WIDTH,
		...m.rungs.map((r) => {
			const a = annotateRung(r, live);
			return rungMinWidth(a.elems, a.coils);
		})
	);
}

const COMMENT_LINE_H = 12;
/** Characters of 10 px text that fit between `x` and the right rail. */
const fits = (W: number, x: number) => (W - x - L.RAIL_RIGHT_MARGIN) / (10 * MONO_ADVANCE);

function annotateRung(r: LdRung, live?: LiveSnapshot): { elems: Ann[]; coils: Ann[] } {
	const resolve = (label: string) => (live ? live.resolve(label, r.scope) : undefined);
	const all = annotate([...r.elements, ...r.coils], live ? true : undefined, resolve);
	return { elems: all.slice(0, r.elements.length), coils: all.slice(r.elements.length) };
}

type Item =
	| { t: 'rung'; line: number; r: LdRung }
	| { t: 'note'; line: number; text: string }
	| { t: 'pou'; line: number; name: string; pins: string[] };

/**
 * Lay the routine out at `width` (at least its natural width — rails span
 * the canvas and coils hug the right rail, as in the editor).
 */
export function ladderDiagram(model: LdModel, width: number, live?: LiveSnapshot): Diagram {
	const m = normalizeLd(model);
	const W = Math.max(width, ladderNaturalWidth(m, live));
	const pen = new Pen();
	const blocks: Block[] = [];

	const items: Item[] = m.rungs.map((r) => ({ t: 'rung' as const, line: r.line, r }));
	for (const c of m.comments ?? []) items.push({ t: 'note', line: c.line, text: c.text });
	for (const b of m.blocks ?? []) {
		items.push({
			t: 'pou',
			line: b.line,
			name: b.name,
			pins: (b.pins ?? []).map((p) => `${p.dir === 'out' ? 'OUT ' : ''}${p.name} : ${p.type}`)
		});
	}
	items.sort((a, b) => a.line - b.line);

	let y = 0;
	for (const it of items) {
		if (it.t === 'pou') {
			// The block's interface wraps under its name.
			const pins = packList(it.pins, (W - 30) / (9 * MONO_ADVANCE));
			const h = POU_H + pins.length * PIN_LINE_H;
			const top = y + 10;
			pen.line(0, top, 0, top + h - 10, { width: 3 });
			pen.text(10, top + 14, 'FUNCTION_BLOCK', { size: 10, bold: true, color: MUTED });
			pen.text(10 + textWidth('FUNCTION_BLOCK ', 10), top + 14, it.name, { size: 12, bold: true });
			pins.forEach((l, i) => pen.text(10, top + 14 + (i + 1) * PIN_LINE_H, l, { size: 9, color: MUTED }));
			blocks.push({ y0: y, y1: y + h + 4, keep: true });
			y += h + 4;
		} else if (it.t === 'note') {
			const lines = wrapText(it.text, fits(W, 36));
			const h = lines.length * NOTE_LINE_H + 8;
			const w = Math.max(...lines.map((l) => textWidth(l, 10))) + 16;
			const top = y + 6;
			pen.rect(20, top, w, h, { rx: 3, s: { color: FAINT, width: 0.8, dash: [3, 2] } });
			lines.forEach((l, i) => pen.text(28, top + 4 + (i + 1) * NOTE_LINE_H - 3, l, { size: 10, italic: true, color: MUTED }));
			blocks.push({ y0: y, y1: top + h + 4, keep: true });
			y = top + h + 4;
		} else {
			const a = annotateRung(it.r, live);
			const lay = layoutRung(a.elems, a.coils, W);
			// The rung's name and comment head it, wrapped: every line past
			// the first pushes the rung down.
			const head = wrapText(it.r.comment ? `${it.r.name} (* ${it.r.comment} *)` : it.r.name, fits(W, L.RAIL_LEFT));
			const extra = (head.length - 1) * COMMENT_LINE_H;
			pen.line(L.RAIL_X, y, L.RAIL_X, y + extra, { width: 3 });
			pen.line(W - L.RAIL_X, y, W - L.RAIL_X, y + extra, { width: 3 });
			head.forEach((l, i) => pen.text(L.RAIL_LEFT, y + 11 + i * COMMENT_LINE_H, l, { size: 10, italic: true, color: MUTED }));
			pen.at(0, y + extra, () => drawRung(pen, it.r, lay, W, live));
			blocks.push({ y0: y, y1: y + extra + lay.height });
			y += extra + lay.height;
		}
	}

	return {
		// A file of ladder FUNCTION_BLOCKs alone (a library) has no program name.
		title: m.name || (m.blocks ?? []).map((b) => b.name).join(', ') || 'Ladder',
		kind: 'Ladder Diagram',
		width: W,
		height: y,
		items: pen.items,
		blocks
	};
}

function wire(on: boolean | undefined) {
	return on === true ? { color: POWER, width: 2.6 } : { color: INK, width: 1.6 };
}

function drawRung(pen: Pen, r: LdRung, lay: ReturnType<typeof layoutRung>, W: number, live?: LiveSnapshot): void {
	// Power rails: drawn per rung, so consecutive rungs join into one rail.
	pen.line(L.RAIL_X, 0, L.RAIL_X, lay.height, { width: 3 });
	pen.line(W - L.RAIL_X, 0, W - L.RAIL_X, lay.height, { width: 3 });
	for (const w of lay.wires) pen.line(w.x1, w.y1, w.x2, w.y2, wire(w.on));
	for (const v of lay.vlines) pen.line(v.x, v.y1, v.x, v.y2, wire(v.on));
	for (const n of lay.nodes) pen.at(n.x, n.y, () => drawNode(pen, r, n, live));
}

function drawNode(pen: Pen, r: LdRung, n: LNode, live?: LiveSnapshot): void {
	const el = n.ann.el;
	const lit = n.ann.val === true;
	const ink = lit ? POWER : INK;
	const post = { color: ink, width: 1.8 };
	const mid = n.w / 2;
	const cy = n.h / 2;
	const operand = (ref: string | undefined) => {
		pen.text(mid, n.h + 12, trunc(ref, OPERAND_LABEL_MAX), { size: 10, anchor: 'middle', color: ink });
		if (live && ref) {
			const v = live.resolve(ref, r.scope);
			if (v !== undefined) pen.text(mid, n.h + 24, live.format(v, ref), { size: 9, anchor: 'middle', italic: true, color: lit ? POWER : MUTED });
		}
	};
	switch (n.kind) {
		case 'contact': {
			pen.line(0, cy, mid - 5, cy, wire(n.ann.in));
			pen.line(mid + 5, cy, n.w, cy, wire(n.ann.out));
			pen.line(mid - 5, 2, mid - 5, n.h - 2, post);
			pen.line(mid + 5, 2, mid + 5, n.h - 2, post);
			if (el.neg) pen.line(mid - 9, n.h - 1, mid + 9, 1, post);
			operand(el.ref);
			break;
		}
		case 'edge': {
			pen.line(0, cy, mid - 8, cy, wire(n.ann.in));
			pen.line(mid + 8, cy, n.w, cy, wire(n.ann.out));
			pen.line(mid - 8, 2, mid - 8, n.h - 2, post);
			pen.line(mid + 8, 2, mid + 8, n.h - 2, post);
			pen.text(mid, cy + 3.5, el.mode === 'N' ? 'N' : 'P', { size: 9, bold: true, anchor: 'middle', color: ink });
			operand(el.ref);
			break;
		}
		case 'coil': {
			pen.line(0, cy, mid - 12, cy, wire(n.ann.in));
			pen.line(mid + 12, cy, n.w, cy, wire(n.ann.val));
			pen.path([['M', mid - 8, 2], ['Q', mid - 16, cy, mid - 8, n.h - 2]], { s: post });
			pen.path([['M', mid + 8, 2], ['Q', mid + 16, cy, mid + 8, n.h - 2]], { s: post });
			if (el.mode) pen.text(mid, cy + 4, el.mode, { size: 9, bold: true, anchor: 'middle', color: ink });
			operand(el.ref);
			break;
		}
		case 'fn':
		case 'assign': {
			pen.rect(0, 0, n.w, n.h, { rx: 4, s: { color: ink, width: 1.2 } });
			const label = n.kind === 'fn' ? `${el.fn}(${el.args ?? ''})` : (el.text ?? '');
			pen.text(mid, cy + 3.5, label, { size: 10, anchor: 'middle', color: ink });
			break;
		}
		case 'fb': {
			pen.rect(0, 0, n.w, n.h, { rx: 3, s: { color: ink, width: 1.2 } });
			pen.text(mid, -4, el.inst ?? '', { size: 10, bold: true, anchor: 'middle', color: ink });
			pen.text(mid, 15, el.type ?? '', { size: 10, bold: true, anchor: 'middle', color: ink });
			if (el.args) pen.text(mid, n.h - 5, fitArgs(el.args), { size: 8.5, anchor: 'middle', color: MUTED });
			if (el.powerIn) pen.text(3, 9, el.powerIn, { size: 7.5, color: MUTED });
			if (el.powerOut) pen.text(n.w - 3, 9, el.powerOut, { size: 7.5, anchor: 'end', color: MUTED });
			break;
		}
	}
}

/** A comma-separated list in lines of at most `max` characters, breaking
 * only between items (`Mode : INT,` never parts from its type). */
export function packList(items: string[], max: number): string[] {
	const out: string[] = [];
	let line = '';
	items.forEach((item, i) => {
		const piece = item + (i < items.length - 1 ? ',' : '');
		if (line && line.length + 1 + piece.length > max) {
			out.push(line);
			line = piece;
		} else line = line ? `${line} ${piece}` : piece;
	});
	if (line) out.push(line);
	return out;
}
