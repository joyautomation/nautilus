// Sequential Function Chart → display list, on the SFC view's own layout
// (sfc.ts layoutSfc): steps with their action tables, transition bars with
// their conditions, loops back drawn as the compact jump glyph. A chart is
// one picture, but a long one may run over several pages: it breaks in the
// gaps between ranks (only flow lines cross a break, never a box, a bar or
// a condition). The ACTION bodies follow the chart as an appendix, since a
// printed chart that names `Fill` is only half the program without them.

import { ASSOC_GAP, G, layoutSfc, normalizeSfc, type SfcModel } from '../sfc.ts';
import { FAINT, INK, MUTED, Pen, POWER, SHADE, textWidth, mergeBlocks, type Block, type Diagram } from './draw.ts';
import type { LiveSnapshot } from './live.ts';

const CODE_LINE_H = 13;

export function sfcDiagram(model: SfcModel, live?: LiveSnapshot): Diagram {
	const m = normalizeSfc(model);
	const lay = layoutSfc(m);
	const pen = new Pen();
	const spans: Block[] = [];
	let width = lay.width;

	for (const n of lay.notes) {
		pen.rect(n.x, n.y, n.w, n.h, { rx: 3, s: { color: FAINT, width: 0.8, dash: [3, 2] } });
		pen.text(n.x + 8, n.y + 19, n.c.text.split('\n')[0], { size: 10, italic: true, color: MUTED });
		spans.push({ y0: n.y - 4, y1: n.y + n.h + 4, keep: true });
	}

	// Labels go down after every line, so a condition's backing covers any
	// stem or bar it sits across — whichever transition drew it.
	const labels: (() => void)[] = [];
	for (const r of lay.trans) {
		const name = r.t.name;
		if (r.jump) {
			const { x, y } = r.jump;
			pen.rect(x - 9, y - 9, 18, 18, { rx: 3, fill: '#ffffff', s: { color: INK, width: r.double ? 2 : 1.2 } });
			// The return arrow (↩), drawn: the PDF's built-in fonts have no glyph for it.
			pen.path([['M', x + 4, y - 4], ['L', x + 4, y + 2], ['L', x - 4, y + 2]], { s: { color: INK, width: 1.2 } });
			pen.path([['M', x - 4, y - 1], ['L', x - 7, y + 2], ['L', x - 4, y + 5], ['Z']], { fill: INK });
			const label = `${r.t.to.join(', ')}: ${r.t.cond}`;
			labels.push(() => {
				pen.text(x + 16, y + 4, label, { size: 11, color: INK }, true);
				if (name) pen.text(x + 16, y - 12, name, { size: 10, bold: true }, true);
				if (r.prio) prio(pen, x - 17, y - 9, r.prio);
			});
			width = Math.max(width, x + 16 + textWidth(label, 11) + G.MARGIN);
			spans.push({ y0: y - (name ? 24 : 12), y1: y + 12 });
			continue;
		}
		for (const leg of [...r.legsIn, ...r.legsOut]) pen.line(leg.x, leg.y1, leg.x, leg.y2, { width: 1.8 });
		pen.line(r.barX1, r.barY, r.barX2, r.barY, { width: 2.6 });
		if (r.double) pen.line(r.barX1, r.barY + 4, r.barX2, r.barY + 4, { width: 2.6 });
		const cond = r.t.cond || '…';
		// The view centres the condition on condY+4 (dominant-baseline:
		// middle); as a plain baseline that sits ~4 px lower.
		labels.push(() => {
			pen.text(r.condX, r.condY + 8, cond, { size: 11, color: INK }, true);
			if (name) pen.text(r.condX, r.condY - 9, name, { size: 10, bold: true }, true);
			if (r.prio) prio(pen, r.barX1 - 14, r.barY, r.prio);
		});
		width = Math.max(width, r.condX + textWidth(cond, 11) + G.MARGIN);
		spans.push({ y0: Math.min(r.barY, r.condY) - (name ? 20 : 10), y1: Math.max(r.barY + (r.double ? 4 : 0), r.condY) + 12 });
	}

	for (const draw of labels) draw();

	for (const p of lay.steps) {
		const active = live ? live.resolve(`_S_${p.step.name}_X`) === true : false;
		pen.at(p.x, p.y, () => {
			pen.rect(0, 0, p.w, p.h, { rx: 4, fill: active ? '#e3f4e8' : '#ffffff', s: { color: active ? POWER : INK, width: active ? 2.4 : 1.4 } });
			if (p.step.initial) pen.rect(4, 4, p.w - 8, p.h - 8, { rx: 2, s: { color: active ? POWER : INK, width: 1.4 } });
			pen.text(p.w / 2, p.h / 2 + 4, p.step.name, { size: 12, anchor: 'middle', bold: true });
			const actions = p.step.actions ?? [];
			if (actions.length) {
				pen.at(p.w + ASSOC_GAP, 0, () => {
					// IEC's action block: qualifier | action, one row each,
					// joined to the step by a short line.
					pen.line(-ASSOC_GAP, 8, 0, 8, { width: 1 });
					pen.rect(0, 0, p.assocW, actions.length * 16 + 2, { s: { color: INK, width: 0.9 } });
					pen.line(22, 0, 22, actions.length * 16 + 2, { color: INK, width: 0.6 });
					actions.forEach((a, i) => {
						if (i > 0) pen.line(0, i * 16 + 1, p.assocW, i * 16 + 1, { color: FAINT, width: 0.5 });
						pen.text(4, i * 16 + 12, a.qualifier, { size: 10, bold: true });
						pen.text(26, i * 16 + 12, a.target + (a.time ? `(${a.time})` : ''), { size: 10 });
					});
				});
			}
		});
		const assocH = (p.step.actions?.length ?? 0) * 16 + 2;
		spans.push({ y0: p.y - 4, y1: p.y + Math.max(p.h, assocH) + 4 });
	}

	for (const o of lay.orphans) {
		pen.rect(o.x, o.y, o.w, o.h, { rx: 4, s: { color: INK, width: 1.2, dash: [4, 2] } });
		pen.text(o.x + 8, o.y + 15, `FROM ${o.t.from.join(', ') || '?'} -> TO ${o.t.to.join(', ') || '?'}`, { size: 10, bold: true });
		pen.text(o.x + 8, o.y + 31, o.t.cond || '…', { size: 10 });
		spans.push({ y0: o.y - 4, y1: o.y + o.h + 4 });
	}

	// The ACTION bodies, below the chart.
	let y = lay.height;
	const actions = m.actions ?? [];
	if (actions.length) {
		pen.text(G.MARGIN, y + 14, 'Actions', { size: 13, bold: true });
		spans.push({ y0: y, y1: y + 22, keep: true });
		y += 26;
		for (const a of actions) {
			const lines = dedent(a.body);
			const h = 22 + lines.length * CODE_LINE_H + 6;
			const w = Math.max(240, textWidth(`ACTION ${a.name}`, 11) + 20, ...lines.map((l) => textWidth(l, 10) + 24));
			pen.rect(G.MARGIN, y, w, h, { rx: 3, s: { color: FAINT, width: 0.8 } });
			pen.rect(G.MARGIN, y, w, 18, { rx: 3, fill: SHADE });
			pen.text(G.MARGIN + 8, y + 13, `ACTION ${a.name}`, { size: 11, bold: true });
			lines.forEach((l, i) => pen.text(G.MARGIN + 12, y + 22 + (i + 1) * CODE_LINE_H - 3, l, { size: 10 }));
			spans.push({ y0: y - 2, y1: y + h + 2 });
			width = Math.max(width, G.MARGIN + w + G.MARGIN);
			y += h + 10;
		}
		y += G.MARGIN - 10;
	}

	return {
		title: m.name || 'sfc',
		kind: 'Sequential Function Chart',
		width,
		height: y,
		items: pen.items,
		blocks: mergeBlocks(spans, 2)
	};
}

/** An ACTION body's lines, tabs expanded and the source's indentation
 * removed (the model's body starts at its first token, so only the lines
 * after it carry the block's indent). */
export function dedent(body: string): string[] {
	const lines = body.replace(/\s+$/, '').replace(/\t/g, '    ').split('\n');
	const indents = lines.slice(1).filter((l) => l.trim() !== '').map((l) => l.length - l.trimStart().length);
	const cut = Math.min(...indents);
	if (!indents.length || !Number.isFinite(cut) || lines[0].length - lines[0].trimStart().length >= cut) return lines;
	return [lines[0], ...lines.slice(1).map((l) => l.slice(Math.min(cut, l.length - l.trimStart().length)))];
}

function prio(pen: Pen, x: number, y: number, n: number): void {
	pen.circle(x, y, 6, { fill: '#ffffff', s: { color: MUTED, width: 0.8 } });
	pen.text(x, y + 3, String(n), { size: 8, bold: true, anchor: 'middle', color: MUTED });
}
