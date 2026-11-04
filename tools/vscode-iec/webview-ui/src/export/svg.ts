// Display list → standalone SVG: presentation attributes only (no CSS, no
// classes, no external fonts), so the file drops into a document, a slide
// or an image editor and looks the same everywhere.

import { INK, PAPER, type Prim, type Seg, type Stroke } from './draw.ts';

export const SVG_FONT = "ui-monospace, 'SF Mono', Menlo, Consolas, 'DejaVu Sans Mono', 'Liberation Mono', 'Courier New', monospace";

export function num(n: number): string {
	const r = Math.round(n * 100) / 100;
	return Object.is(r, -0) ? '0' : String(r);
}

export function esc(s: string): string {
	return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function stroke(s: Stroke | undefined): string {
	if (!s) return ' stroke="none"';
	let out = ` stroke="${s.color ?? INK}" stroke-width="${num(s.width ?? 1)}"`;
	if (s.dash?.length) out += ` stroke-dasharray="${s.dash.map(num).join(' ')}"`;
	return out;
}

function pathData(d: Seg[]): string {
	return d.map((seg) => seg[0] + (seg.length > 1 ? ' ' + (seg.slice(1) as number[]).map(num).join(' ') : '')).join(' ');
}

export function primToSvg(p: Prim): string {
	switch (p.t) {
		case 'line':
			return `<line x1="${num(p.x1)}" y1="${num(p.y1)}" x2="${num(p.x2)}" y2="${num(p.y2)}"${stroke({ ...p.s, color: p.s.color ?? INK })}/>`;
		case 'rect':
			return `<rect x="${num(p.x)}" y="${num(p.y)}" width="${num(p.w)}" height="${num(p.h)}"${p.rx ? ` rx="${num(p.rx)}"` : ''} fill="${p.fill ?? 'none'}"${stroke(p.s)}/>`;
		case 'circle':
			return `<circle cx="${num(p.cx)}" cy="${num(p.cy)}" r="${num(p.r)}" fill="${p.fill ?? 'none'}"${stroke(p.s)}/>`;
		case 'path':
			return `<path d="${pathData(p.d)}" fill="${p.fill ?? 'none'}"${stroke(p.s)}/>`;
		case 'text': {
			let a = `x="${num(p.x)}" y="${num(p.y)}" font-size="${num(p.size)}"`;
			if (p.anchor && p.anchor !== 'start') a += ` text-anchor="${p.anchor}"`;
			if (p.bold) a += ' font-weight="bold"';
			if (p.italic) a += ' font-style="italic"';
			a += ` fill="${p.color ?? INK}"`;
			return `<text ${a} xml:space="preserve">${esc(p.text)}</text>`;
		}
	}
}

/** A whole SVG document of the given size around a body of markup. */
export function svgDocument(width: number, height: number, body: string, title?: string): string {
	const w = num(width);
	const h = num(height);
	return (
		`<?xml version="1.0" encoding="UTF-8"?>\n` +
		`<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" font-family="${esc(SVG_FONT)}">\n` +
		(title ? `<title>${esc(title)}</title>\n` : '') +
		`<rect x="0" y="0" width="${w}" height="${h}" fill="${PAPER}"/>\n` +
		body +
		`\n</svg>\n`
	);
}

export function primsToSvg(items: Prim[]): string {
	return items.map(primToSvg).join('\n');
}
