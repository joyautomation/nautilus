// Diagram export: LD, FBD and SFC models (as `naut ld|fbd|sfc graph` emit
// them) to a print-friendly SVG or a paginated PDF. Pure — no DOM, no
// Node APIs — so it runs in the extension host (bundled to
// media/dist/diagram-export.cjs) and under `node --test` alike, on the
// very layout code the diagram editors draw with.

import type { LdModel } from '../ladder.ts';
import type { FbdModel } from '../layout.ts';
import type { SfcModel } from '../sfc.ts';
import { MUTED, type Diagram } from './draw.ts';
import { fbdDiagram } from './fbdExport.ts';
import { ladderDiagram } from './ladderExport.ts';
import type { LiveSnapshot } from './live.ts';
import { paginate, type PageOptions, type Sheet } from './paginate.ts';
import { pdfDocument } from './pdf.ts';
import { sfcDiagram } from './sfcExport.ts';
import { esc, num, primsToSvg, svgDocument } from './svg.ts';

export { liveSnapshot, type LiveSnapshot } from './live.ts';
export type { Orientation, Paper, PageOptions, Sheet } from './paginate.ts';

export type DiagramLang = 'ld' | 'fbd' | 'sfc';

/** The diagram for a model; `width` is the canvas a ladder spans (0 = its
 * natural width). */
export function diagramFor(lang: DiagramLang, model: unknown, width = 0, live?: LiveSnapshot): Diagram {
	switch (lang) {
		case 'ld':
			return ladderDiagram(model as LdModel, width, live);
		case 'fbd':
			return fbdDiagram(model as FbdModel, live);
		case 'sfc':
			return sfcDiagram(model as SfcModel, live);
	}
}

const TITLE_H = 30;
const EDGE = 12;

/** The whole diagram as one standalone SVG image, at screen size, with a
 * one-line title (POU name, diagram kind) above it. */
export function exportSvg(lang: DiagramLang, model: unknown, opts: { live?: LiveSnapshot } = {}): string {
	const d = diagramFor(lang, model, 0, opts.live);
	const w = d.width + 2 * EDGE;
	const head =
		`<text x="${EDGE}" y="20" font-size="13" font-weight="bold" fill="#000000">${esc(d.title)}</text>\n` +
		`<text x="${num(w - EDGE)}" y="20" font-size="10" text-anchor="end" fill="${MUTED}">${esc(d.kind)}</text>\n`;
	const body = `${head}<g transform="translate(${EDGE} ${TITLE_H})">\n${primsToSvg(d.items)}\n</g>`;
	return svgDocument(w, d.height + TITLE_H + EDGE, body, `${d.title} — ${d.kind}`);
}

/** The diagram laid onto printed sheets. */
export function exportSheets(lang: DiagramLang, model: unknown, opts: PageOptions & { live?: LiveSnapshot }): Sheet[] {
	return paginate((w) => diagramFor(lang, model, w, opts.live), opts);
}

/** One sheet as an SVG page (points as user units) — the PDF's page,
 * drawn the other way. */
export function sheetSvg(s: Sheet, index = 0): string {
	const v = s.view;
	const clip = `clip${index}`;
	const body =
		primsToSvg(s.chrome) +
		`\n<clipPath id="${clip}"><rect x="${num(v.clip.x)}" y="${num(v.clip.y)}" width="${num(v.clip.w)}" height="${num(v.clip.h)}"/></clipPath>` +
		`\n<g clip-path="url(#${clip})"><g transform="translate(${num(v.ox - v.x0 * v.scale)} ${num(v.oy - v.y0 * v.scale)}) scale(${Math.round(v.scale * 10000) / 10000})">\n` +
		primsToSvg(s.items) +
		`\n</g></g>`;
	return svgDocument(s.w, s.h, body);
}

/** The diagram as a paginated PDF. */
export function exportPdf(lang: DiagramLang, model: unknown, opts: PageOptions & { live?: LiveSnapshot }): Uint8Array {
	const sheets = exportSheets(lang, model, opts);
	const d = diagramFor(lang, model, 0);
	return pdfDocument(sheets, `${d.title} - ${d.kind}`);
}
