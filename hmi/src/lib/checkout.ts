// The I/O checkout (#719), as the HMI kit shows it: the client for
// /api/checkout, the rules the page renders from, and the countdown a standing
// checkout force shows. Plain TS (no runes, no DOM), tested in
// tests/checkout.test.ts, because what it must never do is quiet:
//
//   - say an output was released when the controller did not: the countdown
//     follows the controller's own remainingMs from the last frame, a stream
//     gone quiet says "state unknown", and a client timer reaching zero says
//     "ending", never "released";
//   - send a force value the controller would read differently: a digital
//     output is a boolean and an analog one a finite number, typed in JSON,
//     never the text of an input;
//   - call a write whose answer never came a failure or a success: it says it
//     may have been recorded, and to look at the run;
//   - show a reading the page holds instead of the one the controller wrote
//     into the result.
//
// The controller decides every request; this asks the same questions first so
// the operator is not sent round a refusal.

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage, type Me } from './auth.js';

export type PointKind = 'DI' | 'DO' | 'AI' | 'AO';

export interface CheckoutPoint {
	id: string;
	tag: string;
	device?: string;
	address?: string;
	equipment?: string;
	area?: string;
	kind: PointKind;
	expect?: string;
	method: 'observe' | 'force';
	desc?: string;
}

/** One recorded result (`checkout.Result` in Go). */
export interface CheckoutResult {
	run: string;
	seq: number;
	at: number;
	point: string;
	outcome: 'pass' | 'fail' | 'note';
	note?: string;
	/** What the controller read at the tag when it recorded this, and its quality. */
	seen?: unknown;
	quality?: string;
	by: string;
	principal?: string;
	force?: { seq: number; value: unknown; seconds: number; ended?: string };
}

export interface CheckoutRun {
	id: string;
	rehearsal?: boolean;
	state: 'open' | 'abandoned';
	sheet: string;
	title?: string;
	by: string;
	at: number;
	[more: string]: unknown;
}

export interface Progress {
	total: number;
	pass: number;
	fail: number;
	note: number;
	todo: number;
}

/** GET /api/checkout. */
export interface CheckoutView {
	title?: string;
	hash: string;
	signoff: string;
	points: CheckoutPoint[];
	run?: CheckoutRun;
	sheetChanged?: boolean;
	runs: CheckoutRun[];
}

/** GET /api/checkout/runs/{id}. */
export interface CheckoutRunView extends CheckoutRun {
	latest: Record<string, CheckoutResult>;
	byDevice: Record<string, Progress>;
	byArea: Record<string, Progress>;
}

/** What every frame carries while a checkout force stands (`frame.checkoutForce`). */
export interface CheckoutForce {
	run: string;
	point: string;
	tag: string;
	by: string;
	remainingMs: number;
}

export type CheckoutAnswer<T> =
	| { ok: true; value: T }
	| {
			ok: false;
			status: number;
			message: string;
			/** refused: the controller said no; unknown: no answer came, so a write may have been recorded; down: a read failed. */
			outcome: 'refused' | 'unknown' | 'down';
	  };

const UNREACHABLE = 'the controller did not answer';
const MAY_HAVE = 'the controller did not answer: this may have been recorded, so look at the run before doing it again';

export interface CheckoutClientOptions {
	url?: string;
	token?: string;
	fetch?: Fetch;
}

export class CheckoutClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: CheckoutClientOptions = {}) {
		this.#url = opts.url ?? '/api/checkout';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	async #do<T>(path: string, init: RequestInit, failed: string, check: (j: any) => boolean): Promise<CheckoutAnswer<T>> {
		const write = init.method === 'POST';
		const headers: Record<string, string> = {};
		if (this.#token) headers['X-Nautilus-Token'] = this.#token;
		if (write) headers['Content-Type'] = 'application/json';
		let res: Response;
		try {
			res = await this.#fetch(this.#url + path, { ...init, headers });
		} catch {
			return { ok: false, status: 0, message: write ? MAY_HAVE : UNREACHABLE, outcome: write ? 'unknown' : 'down' };
		}
		let text = '';
		try {
			text = await res.text();
		} catch {
			return { ok: false, status: res.status, message: write ? MAY_HAVE : UNREACHABLE, outcome: write ? 'unknown' : 'down' };
		}
		if (!res.ok) {
			// a 5xx on a write may have been applied (the controller says so when it knows)
			const unknown = write && res.status >= 500 && res.headers.get('X-Nautilus-Outcome') === 'unknown';
			return { ok: false, status: res.status, message: refusalMessage(res.status, text, failed), outcome: unknown ? 'unknown' : write ? 'refused' : 'down' };
		}
		let j: any;
		try {
			j = text ? JSON.parse(text) : {};
		} catch {
			return { ok: false, status: res.status, message: 'the controller answered something that is not JSON', outcome: write ? 'unknown' : 'down' };
		}
		if (!check(j)) return { ok: false, status: res.status, message: 'the controller answered something this page does not understand', outcome: write ? 'unknown' : 'down' };
		return { ok: true, value: j as T };
	}

	/** The sheet, the open run and the runs so far. 404: this project has no checkout. */
	view(): Promise<CheckoutAnswer<CheckoutView>> {
		return this.#do('', { method: 'GET' }, 'could not read the checkout', (j) => Array.isArray(j?.points) && Array.isArray(j?.runs));
	}

	run(id: string): Promise<CheckoutAnswer<CheckoutRunView>> {
		return this.#do('/runs/' + encodeURIComponent(id), { method: 'GET' }, 'could not read the run', (j) => typeof j?.id === 'string' && typeof j?.latest === 'object');
	}

	begin(rehearsal: boolean): Promise<CheckoutAnswer<CheckoutRun>> {
		return this.#do('/runs', { method: 'POST', body: JSON.stringify({ rehearsal }) }, 'could not begin the run', (j) => typeof j?.id === 'string');
	}

	record(run: string, point: string, outcome: 'pass' | 'fail' | 'note', note = ''): Promise<CheckoutAnswer<CheckoutResult>> {
		return this.#do(`/runs/${encodeURIComponent(run)}/results`, { method: 'POST', body: JSON.stringify({ point, outcome, note }) }, 'could not record the result', (j) => typeof j?.point === 'string');
	}

	abandon(run: string, reason: string): Promise<CheckoutAnswer<CheckoutRun>> {
		return this.#do(`/runs/${encodeURIComponent(run)}/abandon`, { method: 'POST', body: JSON.stringify({ reason }) }, 'could not abandon the run', (j) => typeof j?.id === 'string');
	}

	/** Force an output for `seconds`: `value` typed (forceValue), `confirm` the point's id as typed. */
	force(run: string, point: string, value: boolean | number, seconds: number, confirm: string): Promise<CheckoutAnswer<Record<string, unknown>>> {
		return this.#do(`/runs/${encodeURIComponent(run)}/force`, { method: 'POST', body: JSON.stringify({ point, value, seconds, confirm }) }, 'could not force the point', (j) => j !== null && typeof j === 'object');
	}

	release(run: string): Promise<CheckoutAnswer<Record<string, unknown>>> {
		return this.#do(`/runs/${encodeURIComponent(run)}/force/release`, { method: 'POST', body: '{}' }, 'could not release the force', (j) => j !== null && typeof j === 'object');
	}
}

/** The force durations the form offers, in seconds (the controller caps at 300). */
export const FORCE_SECONDS: { seconds: number; label: string }[] = [
	{ seconds: 10, label: '10 s' },
	{ seconds: 30, label: '30 s' },
	{ seconds: 60, label: '1 min' },
	{ seconds: 120, label: '2 min' },
	{ seconds: 300, label: '5 min' }
];

const TRUE_WORDS = new Set(['true', 'on', '1', 'yes', 'run', 'start']);
const FALSE_WORDS = new Set(['false', 'off', '0', 'no', 'stop']);

/**
 * What to send as the force value for text typed at a point: a boolean for a
 * digital output, a finite number for an analog one. `error` says what is
 * wrong; the controller still checks the tag's own type.
 */
export function forceValue(kind: PointKind, text: string): { ok: true; value: boolean | number } | { ok: false; error: string } {
	const t = text.trim().toLowerCase();
	if (kind === 'DO') {
		if (TRUE_WORDS.has(t)) return { ok: true, value: true };
		if (FALSE_WORDS.has(t)) return { ok: true, value: false };
		return { ok: false, error: 'a digital output is forced on or off' };
	}
	if (kind === 'AO') {
		if (t === '') return { ok: false, error: 'type the value to force' };
		const n = Number(t);
		if (!Number.isFinite(n)) return { ok: false, error: 'an analog output is forced to a number' };
		return { ok: true, value: n };
	}
	return { ok: false, error: 'an input is observed, never forced' };
}

/** Whether `me` holds `perm` for `area` (everywhere, or in that area). Without a /api/me: no. */
export function canIn(me: Me | null | undefined, perm: string, area: string | undefined): boolean {
	if (!me) return false;
	const p = me.permissions ?? {};
	if (p['*']?.includes(perm)) return true;
	return !!area && !!p[area]?.includes(perm);
}

/** Whether the page offers a force on this point: an output the sheet says is forced. The controller decides the rest. */
export function offersForce(pt: CheckoutPoint): boolean {
	return pt.method === 'force' && (pt.kind === 'DO' || pt.kind === 'AO');
}

export type CountdownState =
	/** a force stands; `remainingMs` is the controller's figure less the time since the frame that said it */
	| { state: 'standing'; remainingMs: number; ending: boolean }
	/** no frame lately: whether the force still stands is not known */
	| { state: 'unknown'; remainingMs: number }
	/** the latest, fresh frame carries no checkout force */
	| { state: 'none' };

/**
 * What to show about a standing checkout force. `force` is the checkoutForce of
 * the latest frame (undefined: that frame had none), `frameAt` when that frame
 * arrived and `now` the time, both from one clock that does not step
 * (performance.now), and `staleMs` how long a quiet stream may be before the
 * answer is "unknown". Only a frame without the field says `none`; reaching
 * zero says `ending`, since release is the controller's to announce.
 */
export function countdown(force: CheckoutForce | null | undefined, frameAt: number, now: number, staleMs = 3000): CountdownState {
	const age = Math.max(now - frameAt, 0);
	if (!force) return age > staleMs ? { state: 'unknown', remainingMs: 0 } : { state: 'none' };
	const left = Math.max(force.remainingMs - age, 0);
	if (age > staleMs) return { state: 'unknown', remainingMs: left };
	return { state: 'standing', remainingMs: left, ending: left === 0 };
}

/** `1:05` for 65 000 ms, rounded up so it never shows 0:00 while time is left. */
export function clock(ms: number): string {
	const s = Math.ceil(Math.max(ms, 0) / 1000);
	return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

/** The banner text for a standing force, or a line saying the state is unknown. */
export function forceBanner(c: CountdownState, force: CheckoutForce | null | undefined): string {
	if (c.state === 'none') return '';
	const who = force ? `${force.tag} is forced by ${force.by} for the I/O checkout` : 'A checkout force may be standing';
	if (c.state === 'unknown') return `${who}: no word from the controller, so whether it still stands is not known`;
	return c.ending ? `${who}: ending now` : `${who}: ${clock(c.remainingMs)} left`;
}

const OUTCOME_LABEL: Record<string, string> = { pass: 'Pass', fail: 'Fail', note: 'Note' };

/** A result's line: the verdict, who, and the reading the controller recorded (never the page's). */
export function resultLine(r: CheckoutResult): string {
	const seen = r.seen === undefined ? 'no reading' : `read ${typeof r.seen === 'object' ? JSON.stringify(r.seen) : String(r.seen)}${r.quality && r.quality !== 'good' ? ` (${r.quality})` : ''}`;
	return `${OUTCOME_LABEL[r.outcome] ?? r.outcome} by ${r.by}, ${seen}${r.note ? `: ${r.note}` : ''}`;
}

/** Whether a note is required (a fail or a note says why). */
export function needsNote(outcome: 'pass' | 'fail' | 'note'): boolean {
	return outcome !== 'pass';
}
