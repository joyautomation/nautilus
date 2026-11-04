// Pagination: a diagram (in diagram pixels) onto printed sheets (in
// points). The diagram is scaled to the sheet's width — never enlarged
// past its on-screen size — and broken into pages between its Blocks, so a
// rung, a network or an SFC rank is never cut in half. A block taller than
// a whole page gets a page of its own, shrunk to fit; only one so tall it
// would be unreadable shrunk is sliced.
//
// Every sheet carries the POU's name and the diagram kind in a header and
// "Page n of N" in its footer.

import { FAINT, MUTED, fitText, mergeBlocks, textWidth, yExtent, type Block, type Diagram, type Prim } from './draw.ts';

export type Paper = 'letter' | 'a4';
export type Orientation = 'portrait' | 'landscape' | 'auto';

export type PageOptions = {
	paper: Paper;
	orientation: Orientation;
	/** Shown in the footer: the source file's name (or path). */
	source?: string;
};

export type Sheet = {
	/** Sheet size in points. */
	w: number;
	h: number;
	/** Header and footer, in points. */
	chrome: Prim[];
	/** Where the diagram's [y0, y1] slice lands: diagram (x, y) maps to
	 * (ox + (x - x0)·scale, oy + (y - y0)·scale), clipped to `clip`. */
	view: { x0: number; y0: number; y1: number; scale: number; ox: number; oy: number; clip: { x: number; y: number; w: number; h: number } };
	/** The primitives that touch this sheet's slice. */
	items: Prim[];
};

const PAPER: Record<Paper, [number, number]> = { letter: [612, 792], a4: [595.28, 841.89] };
const MARGIN = 36; // half an inch
const HEAD = 26;
const FOOT = 18;
/** A CSS pixel is 0.75 pt: at most, the diagram prints at its screen size. */
export const PX = 0.75;
/** Shrinking a tall block below this fraction of the page scale reads
 * worse than slicing it. */
const MIN_SHRINK = 0.6;

export function paperSize(paper: Paper, landscape: boolean): { w: number; h: number } {
	const [a, b] = PAPER[paper];
	return landscape ? { w: b, h: a } : { w: a, h: b };
}

/** The content area's width in diagram pixels at full (screen) size. */
export function contentWidthPx(paper: Paper, landscape: boolean): number {
	return (paperSize(paper, landscape).w - 2 * MARGIN) / PX;
}

/** Portrait unless the diagram is clearly wider than a portrait page. */
export function chooseLandscape(naturalWidth: number, opts: PageOptions): boolean {
	if (opts.orientation !== 'auto') return opts.orientation === 'landscape';
	return naturalWidth > contentWidthPx(opts.paper, false) * 1.15;
}

type Slice = { y0: number; y1: number; scale: number };

/** Break [0, height] into page slices at `scale` (points per pixel) with
 * `capPt` points of content height per page. Exported for tests. */
export function slicePages(blocks: Block[], height: number, scale: number, capPt: number): Slice[] {
	const bl = mergeBlocks(blocks);
	const cap = capPt / scale; // page capacity in diagram pixels
	const out: Slice[] = [];
	let start = 0;
	let i = 0;
	while (i < bl.length) {
		let j = i;
		while (j < bl.length && bl[j].y1 - start <= cap) j++;
		if (j === i) {
			// One block taller than a page.
			const b = bl[i];
			const span = b.y1 - start;
			const fit = capPt / span;
			if (fit >= scale * MIN_SHRINK) {
				out.push({ y0: start, y1: b.y1, scale: fit });
			} else {
				let y = start;
				while (b.y1 - y > cap) {
					out.push({ y0: y, y1: y + cap, scale });
					y += cap;
				}
				out.push({ y0: y, y1: b.y1, scale });
			}
			start = i + 1 < bl.length ? (b.y1 + bl[i + 1].y0) / 2 : b.y1;
			i++;
			continue;
		}
		// A heading or comment is never the last thing on a page.
		while (j < bl.length && j - 1 > i && bl[j - 1].keep) j--;
		if (j < bl.length) {
			const end = (bl[j - 1].y1 + bl[j].y0) / 2;
			out.push({ y0: start, y1: end, scale });
			start = end;
		} else {
			out.push({ y0: start, y1: Math.max(bl[j - 1].y1, Math.min(height, start + cap)), scale });
			start = height;
		}
		i = j;
	}
	if (out.length === 0) out.push({ y0: 0, y1: Math.max(height, 1), scale: Math.min(scale, capPt / Math.max(height, 1)) });
	return out;
}

/**
 * Lay a diagram onto sheets. `build` makes the diagram for a given canvas
 * width (a ladder stretches its rails across the page; the others ignore
 * it) — it is called once to measure, once at the page's width.
 */
export function paginate(build: (width: number) => Diagram, opts: PageOptions): Sheet[] {
	const natural = build(0);
	const landscape = chooseLandscape(natural.width, opts);
	const { w, h } = paperSize(opts.paper, landscape);
	const d = build(contentWidthPx(opts.paper, landscape));
	const contentW = w - 2 * MARGIN;
	const top = MARGIN + HEAD;
	const capPt = h - MARGIN - FOOT - top;
	const scale = Math.min(PX, contentW / d.width);
	const slices = slicePages(d.blocks, d.height, scale, capPt);

	const extents = d.items.map(yExtent);
	return slices.map((s, i) => {
		const drawnW = d.width * s.scale;
		const ox = MARGIN + Math.max(0, (contentW - drawnW) / 2);
		return {
			w,
			h,
			chrome: chrome(d, opts, w, h, i + 1, slices.length),
			view: {
				x0: 0,
				y0: s.y0,
				y1: s.y1,
				scale: s.scale,
				ox,
				oy: top,
				// A hair of slack so a line exactly on a page boundary still
				// shows on both sides of it.
				clip: { x: MARGIN - 2, y: top - 1, w: contentW + 4, h: (s.y1 - s.y0) * s.scale + 2 }
			},
			items: d.items.filter((_, k) => extents[k][1] >= s.y0 - 1 && extents[k][0] <= s.y1 + 1)
		};
	});
}

/** Space kept between the header's title and the diagram kind beside it. */
export const HEADER_GAP = 12;

function chrome(d: Diagram, opts: PageOptions, w: number, h: number, page: number, pages: number): Prim[] {
	// A library of many function blocks has a long title: cut it short of
	// the kind label rather than run into it.
	const titleW = w - 2 * MARGIN - textWidth(d.kind, 9) - HEADER_GAP;
	const out: Prim[] = [
		{ t: 'text', x: MARGIN, y: MARGIN + 12, text: fitText(d.title, 12, titleW), size: 12, bold: true },
		{ t: 'text', x: w - MARGIN, y: MARGIN + 12, text: d.kind, size: 9, anchor: 'end', color: MUTED },
		{ t: 'line', x1: MARGIN, y1: MARGIN + 18, x2: w - MARGIN, y2: MARGIN + 18, s: { color: FAINT, width: 0.5 } },
		{ t: 'line', x1: MARGIN, y1: h - MARGIN - 10, x2: w - MARGIN, y2: h - MARGIN - 10, s: { color: FAINT, width: 0.5 } },
		{ t: 'text', x: w - MARGIN, y: h - MARGIN, text: `Page ${page} of ${pages}`, size: 8, anchor: 'end', color: MUTED }
	];
	if (opts.source) out.push({ t: 'text', x: MARGIN, y: h - MARGIN, text: opts.source, size: 8, color: MUTED });
	return out;
}
