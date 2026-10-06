// Live controller values, fanned out by the extension from the same SSE
// stream that drives the text editor's inline pills — so the diagram obeys
// the identical nautilus.liveValues.enabled toggle and freshness window.
// Nodes read this store directly (like diagState's rects): values tick at
// frame rate without rebuilding the Svelte Flow node array, so drags,
// selections, and open inputs are never disturbed by a data update.

import { resolveLabel, resolveScoped, arrayLowerBounds, member, forcedLabel } from './liveResolve';
import type { VarDecl } from './layout';
// Shared with the extension host (vscode-free): the declared-type lookup the
// text pills and the Live Values panel use, so an enumerated value reads the
// same everywhere (#246).
import { enumText, typeFor, typeLabel, type FlatTypes } from '../../src/tagTypes';

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
	// The controller's force table: lowercased forced address → value.
	// Empty when nothing is forced (or the extension predates forcing).
	forced: {} as Record<string, unknown>,
	// Declared types from the controller's /api/meta, by tagTypes.typeKey:
	// which values are enumerations (they stream as a member's NAME, which
	// alone reads like a STRING). Empty against an older controller.
	types: {} as FlatTypes,
	// Per-dimension array lower bounds by lowercased variable name, parsed
	// from the header declarations — an IEC ARRAY[1..4] stores element [1]
	// at position 0, so indexed chips can't resolve without them.
	bounds: {} as Record<string, number[]>
});

export function setLive(frame: {
	enabled: boolean;
	fresh: boolean;
	values: Record<string, unknown>;
	forced?: Record<string, unknown>;
	types?: FlatTypes;
}): void {
	live.seen = true;
	live.enabled = frame.enabled;
	live.fresh = frame.fresh;
	live.values = frame.values;
	live.forced = frame.forced ?? {};
	// The extension sends the same object until /api/meta is read again;
	// reassigning it anyway is cheap (nothing derives from it but labels).
	live.types = frame.types ?? {};
}

/** True when a diagram label's value is held by a force — the hook every
 * pill uses for its F badge (class "forced" on an .nx-pill; see theme.css). */
export function liveForced(label: string | undefined): boolean {
	return !!label && live.enabled && live.fresh && forcedLabel(live.forced, label);
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

/** True when the value under a diagram label is an enumeration's member
 * (#246) — the hook for the pill's `enum` class (bare, blue italic; see
 * theme.css) and for the type in its tooltip. */
export function liveEnum(label: string | undefined): boolean {
	return !!label && enumText(liveValue(label), typeFor(live.types, label)) !== undefined;
}

/** " (Mode · enum)" for a label whose declared type is known, else "" —
 * appended to a live value's tooltip. */
export function liveTypeNote(label: string | undefined): string {
	const tl = label ? typeLabel(typeFor(live.types, label)) : undefined;
	return tl ? ` (${tl})` : '';
}

/** Compact single-line rendering — mirrors formatValue in src/scan.ts so a
 * value reads the same in the diagram as in the text pill. With `label`, an
 * enumerated value shows as its member's name, bare (#246); a STRING keeps
 * its quotes. */
export function formatLive(v: unknown, label?: string): string {
	if (label) {
		const e = enumText(v, typeFor(live.types, label));
		if (e !== undefined) return e;
	}
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
