// Display list → PDF, written by hand: the export needs lines, rectangles,
// curves and monospace text, and PDF 1.4 says all of that with a dozen
// operators and the built-in Courier faces (no font embedding). That keeps
// the extension free of a PDF library and of a headless browser, and gives
// the same file on macOS, Windows and Linux.

import { INK, type Prim, type Seg, type Stroke } from './draw.ts';
import type { Sheet } from './paginate.ts';

const K = 0.5522847498; // a quarter circle's cubic control distance

function n(v: number): string {
	const r = Math.round(v * 1000) / 1000;
	return Object.is(r, -0) ? '0' : String(r);
}

function rgb(hex: string): [number, number, number] {
	const m = /^#?([0-9a-f]{6})$/i.exec(hex);
	if (!m) return [0, 0, 0];
	const v = parseInt(m[1], 16);
	return [((v >> 16) & 255) / 255, ((v >> 8) & 255) / 255, (v & 255) / 255];
}

const color = (hex: string, op: 'RG' | 'rg') => rgb(hex).map((c) => n(c)).join(' ') + ' ' + op;

// WinAnsiEncoding's 0x80–0x9F block, for the few typographic characters a
// diagram label is likely to carry; Latin-1 maps to itself.
const WIN_ANSI: Record<string, number> = {
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89,
	'Š': 0x8a, '‹': 0x8b, 'Œ': 0x8c, 'Ž': 0x8e, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95,
	'–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9a, '›': 0x9b, 'œ': 0x9c, 'ž': 0x9e, 'Ÿ': 0x9f
};
// Arrows and the like have no WinAnsi glyph: spell them in ASCII.
const ASCII: Record<string, string> = { '→': '->', '←': '<-', '⇢': '->', '↩': '<-', '≥': '>=', '≤': '<=', '≠': '<>', '✕': 'x' };

/** A string as a PDF literal in WinAnsi: escaped, one byte per character. */
export function pdfString(s: string): string {
	let out = '(';
	for (const ch of s) {
		const sub = ASCII[ch];
		if (sub) {
			out += sub;
			continue;
		}
		let code = ch.codePointAt(0)!;
		if (code > 0xff || (code >= 0x80 && code < 0xa0)) code = WIN_ANSI[ch] ?? 0x3f; // '?'
		if (code === 0x28 || code === 0x29 || code === 0x5c) out += '\\' + String.fromCharCode(code);
		else if (code < 0x20) out += ' ';
		else out += String.fromCharCode(code);
	}
	return out + ')';
}

function strokeOps(s: Stroke): string {
	return `${color(s.color ?? INK, 'RG')} ${n(s.width ?? 1)} w [${(s.dash ?? []).map(n).join(' ')}] 0 d`;
}

function paint(fill: string | undefined, s: Stroke | undefined, ops: string, close: boolean): string {
	if (!fill && !s) return '';
	let out = '';
	if (fill) out += color(fill, 'rg') + ' ';
	if (s) out += strokeOps(s) + ' ';
	out += ops + ' ';
	if (fill && s) out += close ? 'b' : 'B';
	else if (fill) out += 'f';
	else out += close ? 's' : 'S';
	return out;
}

function roundRect(x: number, y: number, w: number, h: number, r: number): string {
	r = Math.max(0, Math.min(r, w / 2, h / 2));
	if (r === 0) return `${n(x)} ${n(y)} ${n(w)} ${n(h)} re`;
	const c = r * (1 - K);
	return [
		`${n(x + r)} ${n(y)} m`,
		`${n(x + w - r)} ${n(y)} l`,
		`${n(x + w - c)} ${n(y)} ${n(x + w)} ${n(y + c)} ${n(x + w)} ${n(y + r)} c`,
		`${n(x + w)} ${n(y + h - r)} l`,
		`${n(x + w)} ${n(y + h - c)} ${n(x + w - c)} ${n(y + h)} ${n(x + w - r)} ${n(y + h)} c`,
		`${n(x + r)} ${n(y + h)} l`,
		`${n(x + c)} ${n(y + h)} ${n(x)} ${n(y + h - c)} ${n(x)} ${n(y + h - r)} c`,
		`${n(x)} ${n(y + r)} l`,
		`${n(x)} ${n(y + c)} ${n(x + c)} ${n(y)} ${n(x + r)} ${n(y)} c`,
		'h'
	].join(' ');
}

function segs(d: Seg[]): string {
	let cx = 0;
	let cy = 0;
	const out: string[] = [];
	for (const s of d) {
		switch (s[0]) {
			case 'M':
				out.push(`${n(s[1])} ${n(s[2])} m`);
				[cx, cy] = [s[1], s[2]];
				break;
			case 'L':
				out.push(`${n(s[1])} ${n(s[2])} l`);
				[cx, cy] = [s[1], s[2]];
				break;
			case 'Q': {
				// Degree-elevate the quadratic to the cubic PDF draws.
				const [, qx, qy, x, y] = s;
				out.push(
					`${n(cx + (2 / 3) * (qx - cx))} ${n(cy + (2 / 3) * (qy - cy))} ${n(x + (2 / 3) * (qx - x))} ${n(y + (2 / 3) * (qy - y))} ${n(x)} ${n(y)} c`
				);
				[cx, cy] = [x, y];
				break;
			}
			case 'C':
				out.push(`${n(s[1])} ${n(s[2])} ${n(s[3])} ${n(s[4])} ${n(s[5])} ${n(s[6])} c`);
				[cx, cy] = [s[5], s[6]];
				break;
			case 'Z':
				out.push('h');
				break;
		}
	}
	return out.join(' ');
}

function font(p: { bold?: boolean; italic?: boolean }): string {
	return p.bold ? (p.italic ? '/F4' : '/F2') : p.italic ? '/F3' : '/F1';
}

/** One primitive as content-stream operators, in a y-down coordinate
 * system (the page's CTM flips it). */
export function primToPdf(p: Prim): string {
	switch (p.t) {
		case 'line':
			return paint(undefined, p.s, `${n(p.x1)} ${n(p.y1)} m ${n(p.x2)} ${n(p.y2)} l`, false);
		case 'rect':
			return paint(p.fill, p.s, roundRect(p.x, p.y, p.w, p.h, p.rx ?? 0), true);
		case 'circle': {
			const { cx, cy, r } = p;
			const k = r * K;
			const ops = [
				`${n(cx + r)} ${n(cy)} m`,
				`${n(cx + r)} ${n(cy + k)} ${n(cx + k)} ${n(cy + r)} ${n(cx)} ${n(cy + r)} c`,
				`${n(cx - k)} ${n(cy + r)} ${n(cx - r)} ${n(cy + k)} ${n(cx - r)} ${n(cy)} c`,
				`${n(cx - r)} ${n(cy - k)} ${n(cx - k)} ${n(cy - r)} ${n(cx)} ${n(cy - r)} c`,
				`${n(cx + k)} ${n(cy - r)} ${n(cx + r)} ${n(cy - k)} ${n(cx + r)} ${n(cy)} c`,
				'h'
			].join(' ');
			return paint(p.fill, p.s, ops, true);
		}
		case 'path': {
			const closed = p.d.some((s) => s[0] === 'Z');
			return paint(p.fill, p.s, segs(p.d), closed);
		}
		case 'text': {
			// Courier's advance is exactly 0.6 em, so anchoring is exact.
			const w = [...p.text].reduce((a, ch) => a + (ASCII[ch]?.length ?? 1), 0) * p.size * 0.6;
			const x = p.anchor === 'middle' ? p.x - w / 2 : p.anchor === 'end' ? p.x - w : p.x;
			return `BT ${font(p)} ${n(p.size)} Tf ${color(p.color ?? INK, 'rg')} 1 0 0 -1 ${n(x)} ${n(p.y)} Tm ${pdfString(p.text)} Tj ET`;
		}
	}
}

function sheetContent(s: Sheet): string {
	const out: string[] = [];
	out.push(`q 1 0 0 -1 0 ${n(s.h)} cm`);
	for (const p of s.chrome) out.push(primToPdf(p));
	const v = s.view;
	out.push(`q ${n(v.clip.x)} ${n(v.clip.y)} ${n(v.clip.w)} ${n(v.clip.h)} re W n`);
	out.push(`${n(v.scale)} 0 0 ${n(v.scale)} ${n(v.ox - v.x0 * v.scale)} ${n(v.oy - v.y0 * v.scale)} cm`);
	for (const p of s.items) out.push(primToPdf(p));
	out.push('Q');
	out.push('Q');
	return out.filter((l) => l !== '').join('\n');
}

/** A complete PDF document, one page per sheet, as bytes. */
export function pdfDocument(sheets: Sheet[], title: string): Uint8Array {
	const objs: string[] = [];
	const add = (body: string) => {
		objs.push(body);
		return objs.length; // 1-based object number
	};
	const catalog = add('');
	const pagesObj = add('');
	const fonts = ['Courier', 'Courier-Bold', 'Courier-Oblique', 'Courier-BoldOblique'].map((f) =>
		add(`<< /Type /Font /Subtype /Type1 /BaseFont /${f} /Encoding /WinAnsiEncoding >>`)
	);
	const fontRes = `<< ${fonts.map((id, i) => `/F${i + 1} ${id} 0 R`).join(' ')} >>`;
	const kids: number[] = [];
	for (const s of sheets) {
		const content = sheetContent(s);
		const stream = add(`<< /Length ${content.length} >>\nstream\n${content}\nendstream`);
		kids.push(add(`<< /Type /Page /Parent ${pagesObj} 0 R /MediaBox [0 0 ${n(s.w)} ${n(s.h)}] /Resources << /Font ${fontRes} >> /Contents ${stream} 0 R >>`));
	}
	objs[catalog - 1] = `<< /Type /Catalog /Pages ${pagesObj} 0 R >>`;
	objs[pagesObj - 1] = `<< /Type /Pages /Kids [${kids.map((k) => `${k} 0 R`).join(' ')}] /Count ${kids.length} >>`;
	const info = add(`<< /Title ${pdfString(title)} /Producer (nautilus) >>`);

	let out = '%PDF-1.4\n%\xe2\xe3\xcf\xd3\n';
	const offsets: number[] = [];
	objs.forEach((body, i) => {
		offsets.push(out.length);
		out += `${i + 1} 0 obj\n${body}\nendobj\n`;
	});
	const xref = out.length;
	out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
	for (const off of offsets) out += `${String(off).padStart(10, '0')} 00000 n \n`;
	out += `trailer\n<< /Size ${objs.length + 1} /Root ${catalog} 0 R /Info ${info} 0 R >>\nstartxref\n${xref}\n%%EOF\n`;

	// Every character above is a single byte (pdfString maps to WinAnsi).
	const bytes = new Uint8Array(out.length);
	for (let i = 0; i < out.length; i++) bytes[i] = out.charCodeAt(i) & 0xff;
	return bytes;
}
