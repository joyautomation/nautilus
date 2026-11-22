// TrendChart's pure decision logic — pure, dependency-free, and tested
// (tests/trendchart.test.ts). Everything geometric in TrendChart.svelte stays
// component-local ($derived chains over `pens`/`w`/`height`); what lives here
// is the small set of rules that decide WHAT gets drawn, which is exactly the
// kind of thing that silently misleads an operator if it's wrong.
import type { TrendThreshold } from './types.js';

/**
 * A `TrendThreshold` with no `penId` is chart-level: it applies to the whole
 * chart's shared y-domain rather than one pen's own range. That concept only
 * makes sense in `shared` axis mode — in `percent` mode every pen is
 * normalized to its own 0–100% range, so a raw engineering-units threshold
 * has no single axis to sit on. A threshold WITH a `penId` is active exactly
 * when that pen is currently visible (not hidden via the legend).
 */
export function isThresholdActive(
	th: Pick<TrendThreshold, 'penId'>,
	axisMode: 'shared' | 'percent',
	visiblePenIds: ReadonlySet<string>
): boolean {
	if (th.penId === undefined) return axisMode === 'shared';
	return visiblePenIds.has(th.penId);
}

/**
 * The axis label shown over the y-axis. `yLabel` is an explicit override —
 * needed when pens carry mixed units (so there's no single auto-detected
 * `commonUnit`) or whenever a report chart must always carry the correct
 * engineering-unit label regardless of which pens happen to be visible.
 * `percent` mode's "% of range" is definitional and always wins there.
 */
export function resolveYLabel(
	axisMode: 'shared' | 'percent',
	yLabel: string | undefined,
	commonUnit: string
): string {
	if (axisMode === 'percent') return '% of range';
	return yLabel ?? commonUnit;
}

/** A step of 1, 2 or 5 times a power of ten that is at least `raw`. */
function niceStep(raw: number): number {
	const e = Math.pow(10, Math.floor(Math.log10(raw)));
	const f = raw / e;
	return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * e;
}

/**
 * The y-axis range for data between `min` and `max` when the page fixes
 * neither end: padded, rounded outward to a round step so the four gridline
 * intervals read as round numbers, and never past zero for data that does not
 * go past it (a flow that falls to 0 has an axis starting at 0, not -18.5).
 */
export function niceDomain(min: number, max: number): { lo: number; hi: number } {
	if (!isFinite(min)) min = 0;
	if (!isFinite(max)) max = min + 1;
	const span = max - min || Math.max(Math.abs(max) * 0.1, 1);
	const step = niceStep((span * 1.24) / 4);
	let lo = Math.floor((min - span * 0.12) / step) * step;
	let hi = Math.ceil((max + span * 0.12) / step) * step;
	if (min >= 0 && lo < 0) lo = 0;
	if (max <= 0 && hi > 0) hi = 0;
	if (hi <= lo) hi = lo + step; // data flat at zero: room above it
	return { lo, hi };
}
