// The palette's outline token holds against every ground equipment is
// drawn on (src/lib/palette.css): an outline is the only thing that says a
// stopped pump is there, so --edge must clear WCAG's 3:1 non-text contrast
// on --surface, --bg and --surface-2 in both the dark and the light set.
//
//	npm test
import { readFileSync } from 'node:fs';
import { describe, expect, it } from './harness.js';

const css = readFileSync('src/lib/palette.css', 'utf8') // npm test runs from hmi/
	.replace(/\/\*[\s\S]*?\*\//g, '');

function tokens(selector: string): Record<string, string> {
	const at = css.indexOf(selector);
	if (at < 0) throw new Error(`no ${selector} block in palette.css`);
	const body = css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at));
	return Object.fromEntries([...body.matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6})\s*;/gi)].map((m) => [m[1], m[2]]));
}

function luminance(hex: string): number {
	const [r, g, b] = [1, 3, 5].map((i) => {
		const c = parseInt(hex.slice(i, i + 2), 16) / 255;
		return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
	});
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
	const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
	return (hi + 0.05) / (lo + 0.05);
}

for (const [name, selector] of [
	['dark', '[data-theme=\'dark\'] {'],
	['light', '[data-theme=\'light\'] {']
] as const) {
	describe(`palette ${name}`, () => {
		const t = tokens(selector);
		for (const ground of ['--surface', '--bg', '--surface-2']) {
			it(`--edge is at least 3:1 on ${ground}`, () => {
				expect(contrast(t['--edge'], t[ground]) >= 3).toBe(true);
			});
		}
		it('--axis stays fainter than --edge (it is a chart axis, not an outline)', () => {
			expect(contrast(t['--axis'], t['--surface']) < contrast(t['--edge'], t['--surface'])).toBe(true);
		});
	});
}
