// The audit journal (#709), read: GET /api/audit. No runes, no DOM; tested
// in tests/audit.test.ts. `AuditTable` renders what this returns.
//
// The journal needs the audit permission; a role without it is told so (a
// 403 is not "no entries"). A controller too old to have the journal answers
// 404, and that is said too.

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';

/** One journal entry, as the controller records it. */
export interface AuditEntry {
	id: string;
	seq?: number;
	/** RFC 3339. */
	ts: string;
	/** `user:<name>`, `token`, `token:<name>`, `anonymous`, `system`, … */
	user: string;
	/** Where it came from: `hmi`, `api`, `vscode`, `system`, … */
	source: string;
	/** `tag.write`, `force.set`, `auth.signin`, … */
	action: string;
	target?: string;
	before?: unknown;
	after?: unknown;
	/** `ok`, `refused`, `error`, `unknown`. */
	result: string;
	detail?: Record<string, unknown>;
	from?: string;
	session?: string;
	prev?: string;
}

/**
 * What to ask for. Empty fields match everything; matches are exact, ignoring
 * case. `user` takes the full principal (`user:alice`) or the bare name the
 * Who column shows (`alice`).
 */
export interface AuditFilter {
	user?: string;
	action?: string;
	target?: string;
	result?: string;
	/** From this instant (epoch ms or a Date). */
	from?: number | Date;
	/** Up to this instant. */
	to?: number | Date;
	/** At most this many, newest first. Default (the controller's) 100. */
	limit?: number;
}

export type AuditAnswer =
	| { ok: true; entries: AuditEntry[]; truncated: boolean }
	| { ok: false; status: number; message: string };

/** The query string for a filter (no leading '?'); blank fields left out. */
export function auditQuery(f: AuditFilter = {}): string {
	const q = new URLSearchParams();
	for (const k of ['user', 'action', 'target', 'result'] as const) {
		const v = f[k]?.trim();
		if (v) q.set(k, v);
	}
	for (const k of ['from', 'to'] as const) {
		const v = f[k];
		if (v === undefined) continue;
		const ms = v instanceof Date ? v.getTime() : v;
		if (Number.isFinite(ms)) q.set(k, String(Math.round(ms)));
	}
	if (f.limit !== undefined && f.limit > 0) q.set('limit', String(Math.floor(f.limit)));
	return q.toString();
}

export interface AuditClientOptions {
	/** Default `/api/audit`. */
	url?: string;
	/** `X-Nautilus-Token`, for a cross-origin reader. */
	token?: string;
	/** Default: the kit's fetch (sign-in rules in front, once `createAuth()` started). */
	fetch?: Fetch;
}

export class AuditClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: AuditClientOptions = {}) {
		this.#url = opts.url ?? '/api/audit';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	/** The entries matching `f`, newest first. */
	async query(f: AuditFilter = {}): Promise<AuditAnswer> {
		const qs = auditQuery(f);
		let res: Response;
		try {
			res = await this.#fetch(qs ? `${this.#url}?${qs}` : this.#url, {
				headers: this.#token ? { 'X-Nautilus-Token': this.#token } : {}
			});
		} catch {
			return { ok: false, status: 0, message: 'controller unreachable' };
		}
		const text = await res.text();
		if (!res.ok) {
			if (res.status === 404) return { ok: false, status: 404, message: 'this controller has no audit journal' };
			return { ok: false, status: res.status, message: refusalMessage(res.status, text, 'reading the journal failed') };
		}
		try {
			const j = JSON.parse(text) as { entries?: AuditEntry[]; truncated?: boolean };
			return { ok: true, entries: j.entries ?? [], truncated: !!j.truncated };
		} catch {
			return { ok: false, status: res.status, message: 'the controller answered something that is not JSON' };
		}
	}
}

const show = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));
const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const SHORT = 48;
const clip = (t: string) => (t.length > SHORT ? t.slice(0, SHORT - 1) + '…' : t);

/** A clock time for an epoch-ms instant, in the viewer's zone. */
export function clockText(ms: number): string {
	return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

/** An object as `key value, key value` for one line (values as short JSON). */
function flat(o: Record<string, unknown>): string {
	return clip(
		Object.entries(o)
			.map(([k, v]) => `${k} ${isObj(v) || Array.isArray(v) ? JSON.stringify(v) : show(v)}`)
			.join(', ')
	);
}

/**
 * The value an entry changed, for one line. Plain values read `before → after`;
 * the substitute actions read as the value, who set it and until when; any
 * other object is flattened to `key value, …` and clipped. The whole of it
 * stays available as `changeRaw`.
 */
export function changeText(e: AuditEntry, time: (ms: number) => string = clockText): string {
	const b = isObj(e.before) ? e.before : undefined;
	const a = isObj(e.after) ? e.after : undefined;
	const d = e.detail;
	switch (e.action) {
		case 'substitute.start':
			if (a && 'value' in a) {
				const until = typeof a.untilMs === 'number' ? ` until ${time(a.untilMs)}` : '';
				const live = b && 'value' in b ? ` (live ${show(b.value)})` : '';
				return `${show(a.value)}${until}${live}`;
			}
			break;
		case 'substitute.stop':
			if (b && 'value' in b) {
				const live = 'live' in b ? ` → live ${show(b.live)}` : '';
				return `${show(b.value)}${live}`;
			}
			break;
		case 'substitute.expire':
		case 'substitute.lapse':
			if (b && 'value' in b) {
				const by = typeof b.by === 'string' && b.by ? ` by ${b.by}` : '';
				const until = typeof d?.untilMs === 'number' ? ` until ${time(d.untilMs)}` : '';
				const live = a && 'live' in a ? ` → live ${show(a.live)}` : '';
				return `${show(b.value)}${by}${until}${live}`;
			}
			break;
		case 'alarm.ack':
			if (Array.isArray(e.after)) {
				const ids = e.after.map(show);
				return ids.length ? `acknowledged ${clip(ids.join(', '))}${ids.join(', ').length > SHORT ? ` (${ids.length})` : ''}` : 'nothing to acknowledge';
			}
			break;
	}
	const sb = b ? flat(b) : show(e.before);
	const sa = a ? flat(a) : show(e.after);
	if (!sb && !sa) return '';
	return sb && sa ? `${sb} → ${sa}` : sa || sb;
}

/**
 * The raw before/after as indented JSON, for the expandable detail; '' when
 * `changeText` already says all of it (plain values on both sides).
 */
export function changeRaw(e: AuditEntry): string {
	const structured = (v: unknown) => typeof v === 'object' && v !== null;
	if (!structured(e.before) && !structured(e.after)) return '';
	const o: Record<string, unknown> = {};
	if (e.before !== undefined) o.before = e.before;
	if (e.after !== undefined) o.after = e.after;
	return JSON.stringify(o, null, 2);
}

/** The entry's principal for a person: the user's name, else as recorded. */
export function actorText(e: AuditEntry): string {
	return e.user.startsWith('user:') ? e.user.slice(5) : e.user;
}
