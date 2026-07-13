// Live controller values, fanned out by the extension from the same SSE
// stream that drives the text editor's inline pills — so the diagram obeys
// the identical nautilus.liveValues.enabled toggle and freshness window.
// Nodes read this store directly (like diagState's rects): values tick at
// frame rate without rebuilding the Svelte Flow node array, so drags,
// selections, and open inputs are never disturbed by a data update.

export const live = $state({
	// True once the extension has reported state — the toolbar pill stays
	// hidden until then rather than claiming "off" before we know.
	seen: false,
	enabled: false,
	fresh: false,
	// Top-level keys are lowercased by the extension; struct members inside
	// (FB pins, UDT fields) keep their declared casing.
	values: {} as Record<string, unknown>
});

export function setLive(frame: { enabled: boolean; fresh: boolean; values: Record<string, unknown> }): void {
	live.seen = true;
	live.enabled = frame.enabled;
	live.fresh = frame.fresh;
	live.values = frame.values;
}

/** Resolve a diagram label ("PumpRun", "Motor.Speed", FB pin via liveMember)
 * against the value map, case-insensitively down the accessor path — the
 * same resolution the text scanner applies. undefined = no pill. */
export function liveValue(label: string): unknown {
	if (!live.enabled || !label) return undefined;
	const parts = label.split('.');
	let v: unknown = live.values[parts[0].toLowerCase()];
	for (let i = 1; i < parts.length && v !== undefined; i++) {
		v = member(v, parts[i]);
	}
	return v;
}

/** One case-insensitive member step (an FB output pin off its instance struct). */
export function member(v: unknown, key: string): unknown {
	if (v === null || typeof v !== 'object' || Array.isArray(v)) return undefined;
	const obj = v as Record<string, unknown>;
	if (key in obj) return obj[key];
	const lk = key.toLowerCase();
	const hit = Object.keys(obj).find((k) => k.toLowerCase() === lk);
	return hit === undefined ? undefined : obj[hit];
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
