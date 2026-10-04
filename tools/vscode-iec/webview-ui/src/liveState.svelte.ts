// Live controller values, fanned out by the extension from the same SSE
// stream that drives the text editor's inline pills — so the diagram obeys
// the identical nautilus.liveValues.enabled toggle and freshness window.
// Nodes read this store directly (like diagState's rects): values tick at
// frame rate without rebuilding the Svelte Flow node array, so drags,
// selections, and open inputs are never disturbed by a data update.

import { resolveLabel, resolveScoped, arrayLowerBounds, member } from './liveResolve';
import type { VarDecl } from './layout';

export { member };

export const live = $state({
	// True once the extension has reported state — the toolbar pill stays
	// hidden until then rather than claiming "off" before we know.
	seen: false,
	enabled: false,
	fresh: false,
	// Top-level keys are lowercased by the extension; struct members inside
	// (FB pins, UDT fields) keep their declared casing.
	values: {} as Record<string, unknown>,
	// Per-dimension array lower bounds by lowercased variable name, parsed
	// from the header declarations — an IEC ARRAY[1..4] stores element [1]
	// at position 0, so indexed chips can't resolve without them.
	bounds: {} as Record<string, number[]>
});

export function setLive(frame: { enabled: boolean; fresh: boolean; values: Record<string, unknown> }): void {
	live.seen = true;
	live.enabled = frame.enabled;
	live.fresh = frame.fresh;
	live.values = frame.values;
}

/** Refresh the array-bounds map from the model's header declarations. */
export function setVarBounds(vars: VarDecl[]): void {
	const bounds: Record<string, number[]> = {};
	for (const v of vars) {
		const dims = arrayLowerBounds(v.type);
		if (dims) bounds[v.name.toLowerCase()] = dims;
	}
	live.bounds = bounds;
}

/** Resolve a diagram label — "PumpRun", "Motor.Speed", "TempHist[2]",
 * "m[1][2]", "tbl[i].val" (variable indexes follow their own live value) —
 * against the value map. undefined = no pill. `scope` is a Logix rung's
 * (see resolveScoped); nautilus source has none. */
export function liveValue(label: string, scope?: string): unknown {
	if (!live.enabled || !label) return undefined;
	const v = scope ? resolveScoped(live.values, scope, label) : resolveLabel(live.values, live.bounds, label);
	if (v !== undefined) return v;
	// Word.3: a bit of an integer. The stream carries the word; the bit is
	// read out of it here, the way the runtime reads it.
	const m = /^(.*)\.(\d+)$/.exec(label);
	if (m) {
		const word = scope ? resolveScoped(live.values, scope, m[1]) : resolveLabel(live.values, live.bounds, m[1]);
		if (typeof word === 'number' && Number.isInteger(word)) {
			const bit = Number(m[2]);
			if (bit >= 0 && bit < 64) return ((BigInt(word) >> BigInt(bit)) & 1n) === 1n;
		}
	}
	return undefined;
}

/** True when the live stream is current AND the controller has no tag by
 * this name — the design-time warning for an external READ that would fault
 * the scan (writes create tags; reads of never-written tags error). Only
 * meaningful for VAR_EXTERNAL names; callers gate on the section. */
export function liveMissing(name: string): boolean {
	return live.enabled && live.fresh && !(name.split(/[.[]/)[0].toLowerCase() in live.values);
}

/** Compact single-line rendering — mirrors formatValue in src/scan.ts so a
 * value reads the same in the diagram as in the text pill. */
export function formatLive(v: unknown): string {
	if (v === null || v === undefined) return '—';
	if (typeof v === 'number') {
		if (Number.isInteger(v)) return String(v);
		const abs = Math.abs(v);
		if (abs !== 0 && (abs < 1e-3 || abs >= 1e6)) return v.toExponential(2);
		return v.toFixed(3);
	}
	if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE';
	if (typeof v === 'string') {
		const s = v.length > 32 ? v.slice(0, 29) + '…' : v;
		return JSON.stringify(s);
	}
	if (Array.isArray(v)) return `[${v.length}]`;
	if (typeof v === 'object') return '{…}';
	return String(v);
}
