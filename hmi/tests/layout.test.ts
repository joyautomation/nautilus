// Unit tests for laying a drawing out in its box (src/lib/layout.ts) and
// the built-in drawings' parts (src/lib/drawings.ts), which part-anchored
// ports resolve against.
//
//	npm test
import { describe, expect, it } from './harness.js';
import { fitBox, tracks } from '../src/lib/layout.js';
import { builtinParts, tankGeometry, valveGeometry } from '../src/lib/drawings.js';
import { makeGetPort, portAbsolute, BUILTIN_PORTS } from '../src/lib/mimic.js';

describe('tracks', () => {
	it('gives fixed tracks their size and shares the rest by fr weight', () => {
		const t = tracks(100, [10, '1fr', '3fr', 10]);
		expect(t.map((s) => s.size)).toEqual([10, 20, 60, 10]);
		expect(t.map((s) => s.start)).toEqual([0, 10, 30, 90]);
		expect(t[3].end).toBe(100);
	});
	it('squeezes fr tracks to nothing, never negative, when fixed tracks overflow', () => {
		expect(tracks(15, [10, '1fr', 10]).map((s) => s.size)).toEqual([10, 0, 10]);
	});
});

describe('fitBox', () => {
	it('keeps natural proportions without a height, k is plain uniform scale', () => {
		expect(fitBox(110, undefined, [220, 260])).toEqual({ w: 110, h: 130, k: 0.5 });
	});
	it('follows the shorter side when stretched', () => {
		expect(fitBox(220, 520, [220, 260]).k).toBe(1);
		expect(fitBox(440, 260, [220, 260]).k).toBe(1);
	});
});

describe('built-in drawings', () => {
	it('grows the tank vessel, not its margins, when the tank is made taller', () => {
		const a = tankGeometry(220, undefined, {});
		const b = tankGeometry(220, 400, {});
		expect(b.vessel.x).toBe(a.vessel.x);
		expect(b.vessel.w).toBe(a.vessel.w);
		expect(b.vessel.h - a.vessel.h).toBe(140);
	});
	it('runs the valve body the full width at any size', () => {
		for (const [w, h] of [[90, undefined], [200, 80], [60, 120]] as const) {
			const g = valveGeometry(w, h);
			expect(g.body.x).toBe(0);
			expect(g.body.w).toBe(g.w);
		}
	});
	it('puts the default valve ports on the body ends, mid-body, at any size', () => {
		const box = { x: 100, y: 50, w: 200, h: 80 };
		const parts = builtinParts('Valve', box)!;
		const [inP, outP] = BUILTIN_PORTS.Valve;
		const body = valveGeometry(200, 80).body;
		expect(portAbsolute(box, inP, parts[inP.part!])).toEqual({ x: 100, y: 50 + body.y + body.h / 2, dir: 'left' });
		expect(portAbsolute(box, outP, parts[outP.part!])).toEqual({ x: 300, y: 50 + body.y + body.h / 2, dir: 'right' });
	});
	it('has no parts for a component it does not draw', () => {
		expect(builtinParts('SubmersiblePump', { w: 10, h: 10 })).toBe(undefined);
	});
});

describe('part-anchored ports', () => {
	const box = { x: 10, y: 20, w: 100, h: 50 };
	it('takes fractions of the part rect, offset into the box', () => {
		expect(portAbsolute(box, { x: 1, y: 0.5 }, { x: 20, y: 10, w: 40, h: 20 })).toEqual({ x: 70, y: 40, dir: 'right' });
	});
	it('falls back to the whole box when the part is missing', () => {
		const get = makeGetPort(
			() => box,
			() => [{ name: 'a', part: 'nope', x: 1, y: 0.5 }],
			() => undefined
		);
		expect(get('E', 'a')).toEqual({ x: 110, y: 45, dir: 'right' });
	});
	it('asks getPart for the named part', () => {
		const asked: string[] = [];
		const get = makeGetPort(
			() => box,
			() => [{ name: 'a', part: 'body', x: 0, y: 0 }],
			(eq, part) => {
				asked.push(`${eq}/${part}`);
				return { x: 5, y: 6, w: 1, h: 1 };
			}
		);
		expect(get('E', 'a')).toEqual({ x: 15, y: 26, dir: undefined });
		expect(asked).toEqual(['E/body']);
	});
});
