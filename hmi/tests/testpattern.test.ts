// The playground's virtual camera (#724): a test pattern drawn by the kit.
import { describe, it, expect } from './harness.js';
import { drawTestPattern, sweep, clock, BARS, type Ctx } from '../src/lib/testpattern.js';

function recorder() {
	const rects: Array<{ style: unknown; x: number; y: number; w: number; h: number }> = [];
	const texts: string[] = [];
	const ctx: Ctx = {
		fillStyle: '',
		font: '',
		textBaseline: '',
		fillRect(x, y, w, h) {
			rects.push({ style: ctx.fillStyle, x, y, w, h });
		},
		fillText(t) {
			texts.push(t);
		}
	};
	return { ctx, rects, texts };
}

describe('the test pattern', () => {
	it('draws the seven bars across the width and says what it is', () => {
		const r = recorder();
		drawTestPattern(r.ctx, 700, 400, 1, 'Tank top', new Date(2026, 10, 23, 9, 5, 7));
		expect(r.rects.slice(0, 7).map((x) => x.style)).toEqual([...BARS]);
		expect(r.rects[0].x).toBe(0);
		expect(r.rects[6].x).toBe(600);
		expect(r.texts).toEqual(['Tank top', 'SIMULATED  09:05:07']);
	});
	it('sweeps a line down in four seconds and starts again', () => {
		expect(sweep(0)).toBe(0);
		expect(sweep(2)).toBe(0.5);
		expect(sweep(4)).toBe(0);
		expect(sweep(5)).toBe(0.25);
		expect(sweep(-1)).toBe(0.75);
		const r = recorder();
		drawTestPattern(r.ctx, 700, 400, 2, 'x');
		const line = r.rects[r.rects.length - 1];
		expect(line.y).toBe(200);
	});
	it('writes the clock with two digits each', () => {
		expect(clock(new Date(2026, 0, 2, 3, 4, 5))).toBe('03:04:05');
	});
});
