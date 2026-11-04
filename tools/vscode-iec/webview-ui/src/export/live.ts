// An optional live snapshot for an export: what the diagram showed at the
// moment of export (power flow, values under operands, active steps). Off
// unless asked for — a handout is normally the program, not one moment of
// one controller.

import { resolveLabel, resolveScoped } from '../liveResolve.ts';

export type LiveSnapshot = {
	resolve(label: string, scope?: string): unknown;
	format(v: unknown, label: string): string;
};

/**
 * A snapshot over a flat name → value map (tags and program locals, as
 * the live stream delivers them). Keys are matched case-insensitively,
 * the way every live surface matches them. `format` defaults to a plain
 * rendering; the extension passes its own so enumerations read by name.
 */
export function liveSnapshot(
	values: Record<string, unknown>,
	format?: (v: unknown, label: string) => string
): LiveSnapshot {
	const lower: Record<string, unknown> = {};
	for (const [k, v] of Object.entries(values)) lower[k.toLowerCase()] = v;
	return {
		resolve(label, scope) {
			if (!label) return undefined;
			const v = scope ? resolveScoped(lower, scope, label) : resolveLabel(lower, {}, label);
			if (v !== undefined) return v;
			// Word.3: a bit of an integer, read out of the streamed word.
			const m = /^(.*)\.(\d+)$/.exec(label);
			if (m) {
				const word = scope ? resolveScoped(lower, scope, m[1]) : resolveLabel(lower, {}, m[1]);
				const bit = Number(m[2]);
				if (typeof word === 'number' && Number.isInteger(word) && bit < 53) return Math.floor(word / 2 ** bit) % 2 === 1;
			}
			return undefined;
		},
		format: format ?? plain
	};
}

function plain(v: unknown): string {
	if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE';
	if (typeof v === 'number') return Number.isInteger(v) ? String(v) : String(Number(v.toPrecision(6)));
	if (typeof v === 'string') return v;
	return JSON.stringify(v);
}
