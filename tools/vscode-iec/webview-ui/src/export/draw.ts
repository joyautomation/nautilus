// The export's display list: a handful of vector primitives every diagram
// renderer (ladder, FBD, SFC) draws into, and every backend (SVG, PDF)
// writes out. Keeping the drawing in one small vocabulary is what lets the
// same picture come out as a scalable image and as a paginated PDF without
// a browser, a canvas or a PDF library: lines, rectangles, circles, paths
// of straight and quadratic/cubic segments, and monospace text.
//
// Units are diagram pixels (the webview's own layout units); the PDF
// backend scales them onto points.

export type Stroke = { color?: string; width?: number; dash?: number[] };

export type Seg =
	| ['M', number, number]
	| ['L', number, number]
	| ['Q', number, number, number, number]
	| ['C', number, number, number, number, number, number]
	| ['Z'];

export type TextStyle = {
	size: number;
	anchor?: 'start' | 'middle' | 'end';
	bold?: boolean;
	italic?: boolean;
	color?: string;
};

export type Prim =
	| { t: 'line'; x1: number; y1: number; x2: number; y2: number; s: Stroke }
	| { t: 'rect'; x: number; y: number; w: number; h: number; rx?: number; fill?: string; s?: Stroke }
	| { t: 'circle'; cx: number; cy: number; r: number; fill?: string; s?: Stroke }
	| { t: 'path'; d: Seg[]; fill?: string; s?: Stroke }
	| ({ t: 'text'; x: number; y: number; text: string } & TextStyle);

/** A vertical slice of the diagram that must never be split across pages
 * (a rung, a network, an SFC rank). `keep` holds it on the same page as
 * the block after it — a heading or comment never ends a page alone. */
export type Block = { y0: number; y1: number; keep?: boolean };

export type Diagram = {
	/** The POU's name, printed in the page header. */
	title: string;
	/** "Ladder Diagram", "Function Block Diagram", … */
	kind: string;
	width: number;
	height: number;
	items: Prim[];
	blocks: Block[];
};

// The print palette: black ink on white, one grey for secondary text, and
// one green for power flow when a live snapshot is asked for.
export const INK = '#000000';
export const MUTED = '#555555';
export const FAINT = '#888888';
export const PAPER = '#ffffff';
export const POWER = '#1a7f37';
export const SHADE = '#f2f2f2';

/** A monospace glyph's advance as a fraction of the font size. Courier
 * (the PDF's built-in font) is exactly 0.6 em; the SVG asks for a system
 * monospace face, all of which are within a few percent of it. */
export const MONO_ADVANCE = 0.6;

export function textWidth(s: string, size: number): number {
	return [...s].length * size * MONO_ADVANCE;
}

/** Draw into a display list with a translation stack, so a renderer can
 * place a node's parts in node-local coordinates like the Svelte views do
 * with `<g transform="translate(…)">`. */
export class Pen {
	readonly items: Prim[] = [];
	private ox = 0;
	private oy = 0;
	private stack: [number, number][] = [];

	at(x: number, y: number, draw: () => void): void {
		this.stack.push([this.ox, this.oy]);
		this.ox += x;
		this.oy += y;
		try {
			draw();
		} finally {
			[this.ox, this.oy] = this.stack.pop()!;
		}
	}

	line(x1: number, y1: number, x2: number, y2: number, s: Stroke = {}): void {
		this.items.push({ t: 'line', x1: x1 + this.ox, y1: y1 + this.oy, x2: x2 + this.ox, y2: y2 + this.oy, s });
	}

	rect(x: number, y: number, w: number, h: number, o: { rx?: number; fill?: string; s?: Stroke } = {}): void {
		this.items.push({ t: 'rect', x: x + this.ox, y: y + this.oy, w, h, ...o });
	}

	circle(cx: number, cy: number, r: number, o: { fill?: string; s?: Stroke } = {}): void {
		this.items.push({ t: 'circle', cx: cx + this.ox, cy: cy + this.oy, r, ...o });
	}

	path(d: Seg[], o: { fill?: string; s?: Stroke } = {}): void {
		const ox = this.ox;
		const oy = this.oy;
		const moved = d.map((seg): Seg => {
			switch (seg[0]) {
				case 'M':
				case 'L':
					return [seg[0], seg[1] + ox, seg[2] + oy];
				case 'Q':
					return ['Q', seg[1] + ox, seg[2] + oy, seg[3] + ox, seg[4] + oy];
				case 'C':
					return ['C', seg[1] + ox, seg[2] + oy, seg[3] + ox, seg[4] + oy, seg[5] + ox, seg[6] + oy];
				default:
					return seg;
			}
		});
		this.items.push({ t: 'path', d: moved, ...o });
	}

	/** Text; with `halo`, on a paper-coloured backing so a label that sits
	 * across a line (an SFC condition beside a stem) stays legible. */
	text(x: number, y: number, text: string, style: TextStyle, halo = false): void {
		if (text === '') return;
		if (halo) {
			const w = textWidth(text, style.size);
			const left = style.anchor === 'middle' ? x - w / 2 : style.anchor === 'end' ? x - w : x;
			this.rect(left - 2, y - style.size * 0.8, w + 4, style.size * 1.05, { fill: PAPER });
		}
		this.items.push({ t: 'text', x: x + this.ox, y: y + this.oy, text, ...style });
	}
}

/** A primitive's vertical extent, for deciding which page draws it. */
export function yExtent(p: Prim): [number, number] {
	switch (p.t) {
		case 'line':
			return [Math.min(p.y1, p.y2), Math.max(p.y1, p.y2)];
		case 'rect':
			return [p.y, p.y + p.h];
		case 'circle':
			return [p.cy - p.r, p.cy + p.r];
		case 'path': {
			let lo = Infinity;
			let hi = -Infinity;
			for (const seg of p.d) {
				for (let i = 2; i < seg.length; i += 2) {
					const y = seg[i] as number;
					if (y < lo) lo = y;
					if (y > hi) hi = y;
				}
			}
			return [lo, hi];
		}
		case 'text':
			return [p.y - p.size, p.y + p.size * 0.3];
	}
}

/** Merge overlapping vertical intervals into blocks — the unbreakable
 * slices of a free-form diagram (FBD, SFC). Intervals closer than `slack`
 * merge too; merely touching ones (consecutive rungs) stay apart. */
export function mergeBlocks(spans: Block[], slack = 0): Block[] {
	const sorted = spans.filter((b) => b.y1 >= b.y0).sort((a, b) => a.y0 - b.y0);
	const out: Block[] = [];
	for (const b of sorted) {
		const last = out[out.length - 1];
		if (last && b.y0 < last.y1 + slack) {
			last.y1 = Math.max(last.y1, b.y1);
			// A merged block keeps with the next only if its last part did.
			if (b.y1 >= last.y1) last.keep = b.keep;
		} else out.push({ ...b });
	}
	return out;
}

/** Word-wrap to at most `max` characters a line, keeping the text's own
 * line breaks; a word longer than a line is split. */
export function wrapText(text: string, max: number): string[] {
	const width = Math.max(8, Math.floor(max));
	const out: string[] = [];
	for (const para of text.split('\n')) {
		let line = '';
		for (let word of para.split(/\s+/).filter((w) => w !== '')) {
			while (word.length > width) {
				if (line) {
					out.push(line);
					line = '';
				}
				out.push(word.slice(0, width));
				word = word.slice(width);
			}
			if (!line) line = word;
			else if (line.length + 1 + word.length <= width) line += ' ' + word;
			else {
				out.push(line);
				line = word;
			}
		}
		out.push(line);
	}
	return out;
}

/** Middle-ellipsis truncation, as the ladder view labels long operands. */
export function trunc(s: string | undefined, n: number): string {
	const t = s ?? '';
	return t.length <= n ? t : t.slice(0, Math.ceil(n / 2) - 1) + '…' + t.slice(t.length - Math.floor(n / 2));
}
