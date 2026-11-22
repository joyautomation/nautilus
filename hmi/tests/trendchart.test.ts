// Unit tests for TrendChart's threshold-activity and axis-label rules
// (src/lib/trendchart.ts). The property that matters: a chart-level
// threshold (no `penId`) only ever counts as active in `shared` axis mode —
// `percent` mode has no single engineering-units axis for it to sit on.
import { describe, expect, it } from './harness.js';
import { isThresholdActive, resolveYLabel, niceDomain } from '../src/lib/trendchart.js';

describe('isThresholdActive', () => {
	it('a chart-level threshold (no penId) is active in shared mode', () => {
		expect(isThresholdActive({ penId: undefined }, 'shared', new Set())).toBe(true);
		expect(isThresholdActive({}, 'shared', new Set(['p1']))).toBe(true);
	});

	it('a chart-level threshold is never active in percent mode', () => {
		expect(isThresholdActive({ penId: undefined }, 'percent', new Set(['p1']))).toBe(false);
	});

	it('a per-pen threshold is active only when that pen is currently visible', () => {
		expect(isThresholdActive({ penId: 'p1' }, 'shared', new Set(['p1', 'p2']))).toBe(true);
		expect(isThresholdActive({ penId: 'p1' }, 'shared', new Set(['p2']))).toBe(false);
	});

	it('a per-pen threshold follows the same visibility rule in percent mode', () => {
		expect(isThresholdActive({ penId: 'p1' }, 'percent', new Set(['p1']))).toBe(true);
		expect(isThresholdActive({ penId: 'p1' }, 'percent', new Set())).toBe(false);
	});
});

describe('resolveYLabel', () => {
	it('percent mode always shows "% of range", regardless of yLabel or commonUnit', () => {
		expect(resolveYLabel('percent', 'psi', 'degF')).toBe('% of range');
		expect(resolveYLabel('percent', undefined, '')).toBe('% of range');
	});

	it('shared mode: an explicit yLabel wins over the auto-detected common unit', () => {
		expect(resolveYLabel('shared', 'gpm', 'psi')).toBe('gpm');
	});

	it('shared mode: falls back to the auto-detected common unit when yLabel is omitted', () => {
		expect(resolveYLabel('shared', undefined, 'psi')).toBe('psi');
	});

	it('shared mode with neither set: no label (mixed-unit pens, no override)', () => {
		expect(resolveYLabel('shared', undefined, '')).toBe('');
	});
});

describe('niceDomain', () => {
	it('starts at zero for data that never goes below it (a flow that dropped to 0)', () => {
		const d = niceDomain(0, 154);
		expect(d.lo).toBe(0);
		expect(d.hi >= 154).toBe(true);
		expect(d.hi <= 200).toBe(true);
	});
	it('ends at zero for data that never goes above it', () => {
		const d = niceDomain(-50, 0);
		expect(d.hi).toBe(0);
		expect(d.lo <= -50).toBe(true);
	});
	it('pads data that sits away from zero, to round numbers that hold it', () => {
		const d = niceDomain(40.2, 43.9);
		expect(d.lo <= 40.2 && d.hi >= 43.9).toBe(true);
		expect(d.lo > 30).toBe(true);
		// the gridline step is round: lo and hi are whole multiples of 0.5 here
		expect((d.lo * 2) % 1).toBe(0);
	});
	it('a flat line and no data still make a range', () => {
		const flat = niceDomain(5, 5);
		expect(flat.lo < 5 && flat.hi > 5).toBe(true);
		const none = niceDomain(Infinity, -Infinity);
		expect(none.hi > none.lo).toBe(true);
	});
	it('data crossing zero keeps both sides', () => {
		const d = niceDomain(-10, 30);
		expect(d.lo <= -10 && d.hi >= 30).toBe(true);
	});
});
