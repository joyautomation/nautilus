// The palette's outline token holds against every ground equipment is
// drawn on (src/lib/palette.css): an outline is the only thing that says a
// stopped pump is there, so --edge must clear WCAG's 3:1 non-text contrast
// on --surface, --bg and --surface-2 in both the dark and the light set,
// and every text token clears AA's 4.5:1 on each of them, since naut check
// warns of a screen's text under it.
//
// The state tokens (--good, --warn, --serious, --crit) are measured on the
// same grounds at both bars: 3:1 as a fill, outline or indicator, and 4.5:1
// as text, since the kit also draws them as text (a danger button, an error
// line). A pair that falls short fails at once; a pair that cannot be
// fixed yet is listed below as known, and the list is a ratchet (a listed
// pair that starts to pass fails until its entry is removed).
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

const STATE = ['--good', '--warn', '--serious', '--crit'] as const;
const GROUNDS = ['--surface', '--bg', '--surface-2'] as const;

// State-token pairs below the bar, as "<theme> <token> <ground>". Empty
// since #927 darkened the light-theme tokens and lightened dark --crit.
const SHORT_AS_TEXT = new Set<string>();
const SHORT_AS_NONTEXT = new Set<string>();

const BARS: [number, string, Set<string>][] = [
	[3, 'a fill or outline', SHORT_AS_NONTEXT],
	[4.5, 'text', SHORT_AS_TEXT]
];
const measured = new Set<string>();

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
		for (const text of ['--ink', '--ink-2', '--muted']) {
			for (const ground of ['--surface', '--bg', '--surface-2']) {
				it(`${text} text is at least 4.5:1 on ${ground} (WCAG AA, what naut check asks of a screen)`, () => {
					expect(contrast(t[text], t[ground]) >= 4.5).toBe(true);
				});
			}
		}
		it('--axis stays fainter than --edge (it is a chart axis, not an outline)', () => {
			expect(contrast(t['--axis'], t['--surface']) < contrast(t['--edge'], t['--surface'])).toBe(true);
		});
		for (const state of STATE) {
			for (const ground of GROUNDS) {
				for (const [bar, use, known] of BARS) {
					const key = `${name} ${state} ${ground}`;
					const ratio = contrast(t[state], t[ground]);
					measured.add(`${bar} ${key}`);
					if (known.has(key)) {
						it(`${state} as ${use} on ${ground} is below ${bar}:1 (${ratio.toFixed(2)}), known #927: drop the entry once it passes`, () => {
							expect(ratio < bar).toBe(true);
						});
					} else {
						it(`${state} as ${use} is at least ${bar}:1 on ${ground} (${ratio.toFixed(2)})`, () => {
							expect(ratio >= bar).toBe(true);
						});
					}
				}
			}
		}
	});
}

describe('palette known shortfalls', () => {
	it('name only pairs the test measures', () => {
		const stale = BARS.flatMap(([bar, , known]) => [...known].map((k) => `${bar} ${k}`)).filter((k) => !measured.has(k));
		expect(stale).toEqual([]);
	});
});
