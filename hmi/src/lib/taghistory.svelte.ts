// The last few minutes of a tag's values, for the sparkline beside a
// readout. A mimic binds a value, not a series, so whatever shows a readout
// samples it here (once a second at most per tag, however many readouts
// show it), and every readout of that tag draws the same series. Kept at
// module level, so it survives a page remounting its readouts (switching
// screens and back).
import { SvelteMap } from 'svelte/reactivity';

/** Samples kept per tag: five minutes at one a second. */
export const HISTORY_SAMPLES = 300;

const series = new SvelteMap<string, number[]>();
const last = new Map<string, number>();

/** Add a value to the tag's history: ignored unless it is a finite number,
 * or when the tag was sampled less than a second ago. */
export function sampleTag(name: string, value: unknown, now = Date.now()): void {
	if (!name || typeof value !== 'number' || !isFinite(value)) return;
	const at = last.get(name);
	if (at !== undefined && now - at < 900) return;
	last.set(name, now);
	series.set(name, [...(series.get(name) ?? []), value].slice(-HISTORY_SAMPLES));
}

/** The tag's history, oldest first (empty until it has been sampled). */
export function tagHistory(name: string): number[] {
	return series.get(name) ?? [];
}
