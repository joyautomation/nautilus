// Unit tests for Sparkline's geometry (src/lib/sparkline.ts). The property
// that matters most: never emit NaN into an SVG path, no matter what
// `values` looks like — an empty series, a single point, a flat series, a
// fixed yMin/yMax that the data doesn't actually span.
import { describe, expect, it } from './harness.js';
import { sparklineDomain, sparklineGeometry, sparklineMetrics } from '../src/lib/sparkline.js';

function isFiniteNumber(n: unknown): n is number {
	return typeof n === 'number' && Number.isFinite(n);
}

// A path built from `M x y L x y ...` tokens — pull every numeric token out
// and assert every single one is finite. Catches a NaN/Infinity anywhere in
// the string, not just at the ends.
function pathNumbersAreFinite(d: string): boolean {
	const nums = d.split(/\s+/).filter((t) => t !== 'M' && t !== 'L' && t !== '');
	if (nums.length === 0) return true;
	return nums.every((t) => isFiniteNumber(Number(t)) && t !== 'NaN');
}

describe('sparklineDomain', () => {
	it('an empty series falls back to [0, 1] rather than propagating Infinity', () => {
		expect(sparklineDomain([])).toEqual({ lo: 0, hi: 1 });
	});

	it('yMin/yMax win outright, even when the data does not span them', () => {
		expect(sparklineDomain([5, 5, 5], 0, 10)).toEqual({ lo: 0, hi: 10 });
	});

	it('one side fixed, the other auto-fit from the series', () => {
		expect(sparklineDomain([1, 4, 2], 0, undefined)).toEqual({ lo: 0, hi: 4 });
		expect(sparklineDomain([1, 4, 2], undefined, 10)).toEqual({ lo: 1, hi: 10 });
	});

	it('a flat series (or a single value) is widened by +/-0.5, not a zero-width domain', () => {
		expect(sparklineDomain([7])).toEqual({ lo: 6.5, hi: 7.5 });
		expect(sparklineDomain([3, 3, 3])).toEqual({ lo: 2.5, hi: 3.5 });
	});

	it('non-finite explicit bounds fall back to [0, 1] rather than an Infinity/NaN domain', () => {
		expect(sparklineDomain([1, 2], Infinity, undefined)).toEqual({ lo: 0, hi: 1 });
		expect(sparklineDomain([1, 2], undefined, -Infinity)).toEqual({ lo: 0, hi: 1 });
	});
});

describe('sparklineMetrics', () => {
	it('compact mode is tighter on every axis', () => {
		const normal = sparklineMetrics(false);
		const compact = sparklineMetrics(true);
		expect(compact.xInset < normal.xInset).toBe(true);
		expect(compact.yInset < normal.yInset).toBe(true);
		expect(compact.strokeWidth < normal.strokeWidth).toBe(true);
	});
});

describe('sparklineGeometry — empty and single-point safety', () => {
	it('an empty series draws no path and no single point', () => {
		const g = sparklineGeometry([], { width: 240, height: 44 });
		expect(g.path).toBe('');
		expect(g.singlePoint).toBeNull();
	});

	it('a single value draws a dot, not a degenerate zero-length path', () => {
		const g = sparklineGeometry([12], { width: 240, height: 44 });
		expect(g.path).toBe('');
		expect(g.singlePoint !== null).toBe(true);
		expect(isFiniteNumber(g.singlePoint!.x)).toBe(true);
		expect(isFiniteNumber(g.singlePoint!.y)).toBe(true);
	});

	it('a static single point is centered horizontally (no scroll position to inherit)', () => {
		const g = sparklineGeometry([12], { width: 240, height: 44 });
		expect(g.singlePoint!.x).toBe(120);
	});

	it('two or more values draw a path with every coordinate finite', () => {
		const g = sparklineGeometry([1, 5, 2, 9, 3], { width: 240, height: 44 });
		expect(g.singlePoint).toBeNull();
		expect(g.path.startsWith('M')).toBe(true);
		expect(pathNumbersAreFinite(g.path)).toBe(true);
	});

	it('a flat multi-point series (all equal) still produces finite coordinates', () => {
		const g = sparklineGeometry([4, 4, 4, 4], { width: 240, height: 44 });
		expect(pathNumbersAreFinite(g.path)).toBe(true);
	});

	it('yMin/yMax hold the scale fixed even as new samples arrive outside the old data range', () => {
		const before = sparklineGeometry([1, 2, 3], { width: 100, height: 40, yMin: 0, yMax: 10 });
		const after = sparklineGeometry([1, 2, 3, 9], { width: 100, height: 40, yMin: 0, yMax: 10 });
		// The y for value=1 (first sample, unchanged) must be identical in both —
		// the axis didn't rescale out from under it when a bigger value arrived.
		const yAt = (d: string) => Number(d.split(' ')[2]);
		expect(yAt(before.path)).toBe(yAt(after.path));
	});

	it('scrolling mode (endIndex given) never divides by zero even with windowSize 1', () => {
		const g = sparklineGeometry([1, 2, 3], { width: 100, height: 40, endIndex: 42, windowSize: 1 });
		expect(pathNumbersAreFinite(g.path)).toBe(true);
		expect(isFiniteNumber(g.tx)).toBe(true);
	});

	it('compact mode uses tighter insets than the default', () => {
		const normal = sparklineGeometry([1, 5], { width: 100, height: 40 });
		const compact = sparklineGeometry([1, 5], { width: 100, height: 40, compact: true });
		const firstX = (d: string) => Number(d.split(' ')[1]);
		expect(firstX(compact.path) < firstX(normal.path)).toBe(true);
	});
});
