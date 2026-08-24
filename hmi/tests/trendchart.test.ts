// Unit tests for TrendChart's threshold-activity and axis-label rules
// (src/lib/trendchart.ts). The property that matters: a chart-level
// threshold (no `penId`) only ever counts as active in `shared` axis mode —
// `percent` mode has no single engineering-units axis for it to sit on.
import { describe, expect, it } from './harness.js';
import { isThresholdActive, resolveYLabel } from '../src/lib/trendchart.js';

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
