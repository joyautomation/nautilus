// Data quality, made renderable — the one place the kit decides what an
// operator is shown when a number cannot be fully vouched for.
//
// `RealtimeClient.quality(tag)` answers *what the controller says*. This
// module answers *what to draw*, which is a different and slightly larger
// question, because two facts sit outside the controller's quality axis:
//
//   - the point is not published AT ALL (`present === false`). The runtime has
//     never heard of it. A legacy HMI drew a 3.5px magenta border here; the
//     kit draws `--q-notpublished` and the words "no data". This is NOT the
//     same as bad quality — bad means "I have a value and it is wrong",
//     not-published means "there is no value".
//   - the value is SIMULATED — substituted in the controller upstream of
//     alarming, so every downstream indication is honest about a number that
//     is not the plant's. `--q-simulated`.
//
// Pure, dependency-free, and tested (tests/quality.test.ts): every rule here
// is one an operator's trust rests on, and a component test would not reach
// the precedence order at all.
import type { Quality } from './types.js';

/**
 * What a value primitive renders. A superset of `Quality`: it folds in the two
 * facts above and collapses `notConnected` into `bad`, because to an operator
 * "the source is down" and "the source says this is wrong" call for the same
 * response — do not trust the number, do not command on it.
 */
export type ValueStatus = 'good' | 'stale' | 'bad' | 'notPublished' | 'simulated';

export interface StatusMeta {
	/** The word shown on the badge. Colour is never the only cue. */
	label: string;
	/** Long form, for a title/tooltip. */
	description: string;
	/** The token carrying this status' colour. */
	token: string;
	/**
	 * Whether the value is qualified in any way. `false` only for `good`.
	 * A write control disables on `degraded && !simulated` — see `writable`.
	 */
	degraded: boolean;
	/**
	 * Whether a command may still be sent. Simulation does NOT disable a
	 * control (the plan is explicit: commands stay enabled and the footer
	 * carries a persistent "SIMULATED — not commanding the plant" banner);
	 * stale, bad and not-published all do.
	 */
	writable: boolean;
}

export const STATUS_META: Record<ValueStatus, StatusMeta> = {
	good: {
		label: 'Good',
		description: 'Current value from a healthy source',
		token: 'var(--q-good)',
		degraded: false,
		writable: true
	},
	stale: {
		label: 'Stale',
		description: 'Was good, is no longer being refreshed',
		token: 'var(--q-stale)',
		degraded: true,
		writable: false
	},
	bad: {
		label: 'Bad',
		description: 'The source cannot vouch for this value',
		token: 'var(--q-bad)',
		degraded: true,
		writable: false
	},
	notPublished: {
		label: 'No data',
		description: 'This point is not being published',
		token: 'var(--q-notpublished)',
		degraded: true,
		writable: false
	},
	simulated: {
		label: 'Sim',
		description: 'Simulated — substituted upstream of alarming, not the plant',
		token: 'var(--q-simulated)',
		degraded: true,
		writable: true
	}
};

export interface StatusInput {
	/** False when the runtime does not publish this tag at all. */
	present?: boolean;
	/** As reported by `RealtimeClient.quality(tag)`. */
	quality?: Quality;
	/** The equipment's own SIMULATE member. */
	simulated?: boolean;
}

/**
 * Resolve what to draw, worst fact first.
 *
 * Precedence — **absence beats wrongness beats substitution beats age**:
 *
 *   notPublished > bad > simulated > stale > good
 *
 * `simulated` outranks `stale` deliberately: a simulated value is being
 * refreshed by the simulator, so "stale" would be the less true of the two
 * words, and "this is not the plant" is the more urgent one. It sits below
 * `bad` for the mirror reason — a simulated value the source has flagged bad
 * is bad first.
 */
export function valueStatus(input: StatusInput): ValueStatus {
	if (input.present === false) return 'notPublished';
	const q = input.quality ?? 'good';
	if (q === 'bad' || q === 'notConnected') return 'bad';
	if (input.simulated) return 'simulated';
	if (q === 'stale') return 'stale';
	return 'good';
}

/** Sugar over `valueStatus` — "may a command be sent against this value?" */
export function isWritable(input: StatusInput): boolean {
	return STATUS_META[valueStatus(input)].writable;
}

export interface FormatOptions {
	/** Decimal places for a numeric value. Default 1. */
	precision?: number;
	/** Rendered when there is no value to show. Default `'—'`. */
	placeholder?: string;
	/** Booleans render as words, never as `true`/`false`. */
	trueText?: string;
	falseText?: string;
}

export interface FormattedValue {
	/** What to print. Never empty — falls back to `placeholder`. */
	text: string;
	/** False when there was nothing to print (so a caller can style it). */
	present: boolean;
	/** True when `text` is a number and should be set in the mono readout. */
	numeric: boolean;
}

/**
 * One value → one string, with the "never a confident zero" rule baked in.
 *
 * `undefined`, `null`, `NaN` and `±Infinity` all render as the placeholder,
 * NOT as `0` and not as `NaN`: a screen bound to a point the runtime does not
 * publish must show a dash. This is the single most-repeated three lines in
 * every HMI port, and getting it wrong puts a plausible number on a dead tag.
 */
export function formatValue(
	value: number | string | boolean | null | undefined,
	opts: FormatOptions = {}
): FormattedValue {
	const placeholder = opts.placeholder ?? '—';
	if (value === null || value === undefined) return { text: placeholder, present: false, numeric: false };
	if (typeof value === 'boolean') {
		return { text: value ? (opts.trueText ?? 'On') : (opts.falseText ?? 'Off'), present: true, numeric: false };
	}
	if (typeof value === 'number') {
		if (!Number.isFinite(value)) return { text: placeholder, present: false, numeric: false };
		return { text: value.toFixed(opts.precision ?? 1), present: true, numeric: true };
	}
	const s = String(value);
	return s.length ? { text: s, present: true, numeric: false } : { text: placeholder, present: false, numeric: false };
}

/**
 * Age of a reading, in the terse form a stale badge carries (`45s`, `12m`,
 * `3h`, `2d`). Returns `''` for a non-finite or negative age, so a caller can
 * render the badge without the age rather than the badge with `NaN`.
 */
export function formatAge(ms: number | undefined): string {
	if (ms === undefined || !Number.isFinite(ms) || ms < 0) return '';
	const s = Math.floor(ms / 1000);
	if (s < 60) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m`;
	if (s < 86400) return `${Math.floor(s / 3600)}h`;
	return `${Math.floor(s / 86400)}d`;
}
