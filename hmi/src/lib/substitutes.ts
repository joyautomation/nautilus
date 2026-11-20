// Substitute values (#707): the client for /api/substitutes, and the pure
// rules the panel renders from. No runes, no DOM — tested in
// tests/substitutes.test.ts with a fake fetch, because the contract it
// honours is a safety one:
//
//   - every user action carries ONE requestId, generated before the first
//     send and reused on every retry of that action (never regenerated), so
//     a request whose answer was lost cannot start a second substitute;
//   - a 503 "outcome unknown" is NOT a failure and NOT a success: the
//     controller may still apply it. The caller must re-read and say so;
//   - `kept: false` means it took effect but was not saved — it says so in
//     its warning, and must be shown, not swallowed;
//   - a 409 means the request lost to another change: re-read and decide
//     again, never retry blind.

import { kitFetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';
export interface SubstituteEntry {
	/** A number on the wire; send it back unchanged. */
	id: number;
	name: string;
	value: unknown;
	live?: unknown;
	liveQuality?: string;
	by?: string;
	startMs: number;
	untilMs: number;
	grantedMs?: number;
	remainingMs: number;
	ackOnStart?: boolean;
	acked?: boolean;
	fault?: string;
	requestId?: string;
	role?: string;
	maxMs?: number;
	operatorValue?: boolean;
}

export interface SubstituteRule {
	tag: string;
	value?: unknown;
	maxMs?: number;
	role?: string;
	ackOnStart?: boolean;
	operatorValue?: boolean;
	rule?: string;
}

/**
 * A start a redundant pair's log committed too late to carry out (#710):
 * `ended` false, it never started ("lapsed (outcome was unknown; not
 * started)"); `ended` true, it had started and was ended at a takeover
 * (its commit could not be confirmed). `note` says which.
 */
export interface SubstituteLapse {
	id: number;
	name: string;
	value: unknown;
	by?: string;
	requestId?: string;
	proposedMs: number;
	atMs: number;
	ended?: boolean;
	note: string;
}

export interface SubstituteList {
	substitutes: SubstituteEntry[];
	rules: SubstituteRule[];
	/** Recent lapsed starts (a redundant pair); absent when there are none. */
	lapsed?: SubstituteLapse[];
}

/** Why an action did not (certainly) happen. */
export type SubstituteFailure =
	| 'invalid' // 400: the request is wrong
	| 'forbidden' // 403 (e.g. an operator value the rule does not allow)
	| 'notFound' // 404: not active (already ended)
	| 'conflict' // 409: reused requestId, superseded, or nothing to ack — re-read, decide again
	| 'noLeader' // 503: standby / no leader — nothing was started
	| 'noMajority' // 503: the operator-intent log has no majority (a pair's peer down) — nothing was changed
	| 'notSaved' // 503: the retain store refused — nothing was started
	| 'unknown' // 503 outcome unknown — it MAY still take effect: re-read
	| 'unreachable' // network: no answer at all, the same as unknown
	| 'pending'; // not sent: an earlier start of this tag has an unknown outcome

export type SubstituteResult =
	| {
			ok: true;
			/** False: it took effect but was not saved (see `warning`). */
			kept: boolean;
			warning?: string;
			body: Record<string, unknown>;
			requestId?: string;
	  }
	| {
			ok: false;
			kind: SubstituteFailure;
			message: string;
			/** The same requestId again is the right retry (answer lost / unknown). */
			retrySameId: boolean;
			/** The action may still have taken effect: re-read the list. */
			maybeApplied: boolean;
			requestId?: string;
	  };

export interface SubstituteClientOptions {
	/** Default `/api/substitutes`. */
	url?: string;
	token?: string;
	/** Injectable for tests. */
	fetch?: typeof fetch;
	/** Injectable for tests. */
	uuid?: () => string;
}

export const OUTCOME_HEADER = 'X-Nautilus-Outcome';

function newUuid(): string {
	const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
	if (c?.randomUUID) return c.randomUUID();
	// Insecure-context fallback (plain http on a LAN): 128 random bits.
	const b = new Uint8Array(16);
	if (c && 'getRandomValues' in c) (c as unknown as Crypto).getRandomValues(b);
	else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
	b[6] = (b[6] & 0x0f) | 0x40;
	b[8] = (b[8] & 0x3f) | 0x80;
	const h = [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/**
 * Map a non-2xx answer to the contract's failure kinds.
 *
 * Only an answer carrying `X-Nautilus-Outcome: refused` is CERTAIN: the
 * server refused it before changing anything. Everything else is UNKNOWN —
 * a 503 or 504 from a proxy, a 500, a 502, a 503 without the header — because
 * it can come after the leader applied the change. Exported for tests.
 */
export function classify(status: number, text: string, outcomeHeader: string | null): { kind: SubstituteFailure; message: string } {
	const message = refusalMessage(status, text);
	// A 401 or 403 is the controller refusing who asked, before anything
	// ran, whatever an older controller's header says (it sent none).
	if (status === 401 || status === 403) return { kind: 'forbidden', message };
	if ((outcomeHeader ?? '').toLowerCase() !== 'refused') return { kind: 'unknown', message };
	const lower = message.toLowerCase();
	if (status === 503) {
		if (lower.includes('no majority')) return { kind: 'noMajority', message };
		return { kind: lower.includes('no leader') ? 'noLeader' : 'notSaved', message };
	}
	if (status === 409) return { kind: 'conflict', message };
	if (status === 404) return { kind: 'notFound', message };
	if (status === 403 || status === 401) return { kind: 'forbidden', message };
	return { kind: 'invalid', message };
}

export class SubstituteClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: typeof fetch;
	readonly #uuid: () => string;

	constructor(opts: SubstituteClientOptions = {}) {
		this.#url = opts.url ?? '/api/substitutes';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
		this.#uuid = opts.uuid ?? newUuid;
	}

	/** A fresh id for ONE user action. Keep it for every retry of that action. */
	newRequestId(): string {
		return this.#uuid();
	}

	#headers(json: boolean): Record<string, string> {
		const h: Record<string, string> = {};
		if (json) h['Content-Type'] = 'application/json';
		if (this.#token) h['X-Nautilus-Token'] = this.#token;
		return h;
	}

	async list(): Promise<SubstituteList | { error: string }> {
		try {
			const res = await this.#fetch(this.#url, { headers: this.#headers(false) });
			if (!res.ok) return { error: refusalMessage(res.status, await res.text(), 'read failed') };
			const j = (await res.json()) as Partial<SubstituteList>;
			return { substitutes: j.substitutes ?? [], rules: j.rules ?? [] };
		} catch {
			return { error: 'controller unreachable' };
		}
	}

	/**
	 * Start a substitute. `requestId` is generated here when omitted; to RETRY
	 * an action pass the `requestId` the first attempt returned.
	 */
	start(name: string, value: unknown, seconds: number | undefined, requestId: string = this.newRequestId()): Promise<SubstituteResult> {
		const body: Record<string, unknown> = { name, requestId };
		if (value !== undefined) body.value = value;
		if (seconds !== undefined) body.seconds = seconds;
		return this.#send(this.#url, 'POST', body, requestId);
	}

	/** End a substitute early. `kept: false` = took effect, not saved. */
	stop(id: number | string): Promise<SubstituteResult> {
		return this.#send(`${this.#url}/${encodeURIComponent(id)}`, 'DELETE', undefined, undefined);
	}

	/** Acknowledge substitutes started with ackOnStart. */
	ack(ids: Array<number | string>): Promise<SubstituteResult> {
		return this.#send(`${this.#url}/ack`, 'POST', { ids }, undefined);
	}

	async #send(url: string, method: string, body: Record<string, unknown> | undefined, requestId: string | undefined): Promise<SubstituteResult> {
		let res: Response;
		try {
			res = await this.#fetch(url, {
				method,
				headers: this.#headers(body !== undefined),
				body: body === undefined ? undefined : JSON.stringify(body)
			});
		} catch {
			// No answer: the request may have arrived. Same as unknown.
			return {
				ok: false,
				kind: 'unreachable',
				message: 'controller unreachable — the request may or may not have taken effect',
				retrySameId: requestId !== undefined,
				maybeApplied: true,
				requestId
			};
		}
		if (res.ok) {
			let j: Record<string, unknown> = {};
			try {
				j = ((await res.json()) ?? {}) as Record<string, unknown>;
			} catch {
				/* an empty body is fine */
			}
			const kept = j.kept !== false; // absent = saved; only an explicit false is "not saved"
			const warning = typeof j.warning === 'string' && j.warning ? j.warning : undefined;
			return { ok: true, kept, warning, body: j, requestId };
		}
		const { kind, message } = classify(res.status, await res.text().catch(() => ''), res.headers?.get?.(OUTCOME_HEADER) ?? null);
		return {
			ok: false,
			kind,
			message,
			// Only an unknown START is retried, under the same id. Stop and ack
			// carry no id: re-read and decide. A 409 or a certain 503 likewise.
			retrySameId: kind === 'unknown' && requestId !== undefined,
			maybeApplied: kind === 'unknown',
			requestId
		};
	}
}

/** What the operator reads for a failure; never says "failed" for an unknown. */
export function failureText(r: Extract<SubstituteResult, { ok: false }>): string {
	switch (r.kind) {
		case 'unknown':
		case 'unreachable':
			return `Not confirmed — it may still take effect. Re-read the list; retry a start only with the same request, or wait. (${r.message})`;
		case 'conflict':
			return `Not applied — another change got there first. Re-read and decide again. (${r.message})`;
		case 'noLeader':
			return `Nothing was started — this controller has no leader to take the change. (${r.message})`;
		case 'notSaved':
			return `Nothing was started — the change could not be saved. (${r.message})`;
		case 'noMajority':
			return `Nothing was changed — the operator-intent log has no majority, so substitutes cannot be started, stopped or acknowledged until the other controller is back. (${r.message})`;
		default:
			return r.message;
	}
}

/** Warning line for a `kept: false` success: it IS in effect, but not saved. */
export function keptText(r: Extract<SubstituteResult, { ok: true }>): string | undefined {
	if (r.kept) return undefined;
	return r.warning ?? 'In effect, but not saved: it may not survive a restart or takeover.';
}

/** `1h 02m`, `4m 05s`, `12s`, `ended` — a countdown, never negative. */
export function formatRemaining(ms: number | undefined): string {
	if (ms === undefined || !Number.isFinite(ms)) return '—';
	const s = Math.ceil(ms / 1000);
	if (s <= 0) return 'ending';
	if (s < 60) return `${s}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`;
	return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
}

/** Substitutes still waiting for an ack (started with ackOnStart). */
export function unacked(list: SubstituteEntry[]): SubstituteEntry[] {
	return list.filter((s) => s.ackOnStart && !s.acked);
}

/** Longest allowed duration, in seconds, for a rule (0 = no cap declared). */
export function maxSeconds(rule: SubstituteRule | undefined): number {
	return rule?.maxMs ? Math.floor(rule.maxMs / 1000) : 0;
}

/** Validate a start form: what the server would refuse, caught first. */
export function validateStart(rule: SubstituteRule | undefined, value: unknown, seconds: number): string | null {
	if (!rule) return 'Pick a tag.';
	if (!Number.isFinite(seconds) || seconds <= 0) return 'Enter a duration in seconds.';
	const max = maxSeconds(rule);
	if (max && seconds > max) return `At most ${formatRemaining(rule.maxMs)} for ${rule.tag}.`;
	// A rule with operator-value may be started without one: the server then
	// holds the rule's own value. A rule without it refuses any other value.
	void value;
	return null;
}

/** A start whose outcome is unknown: kept with its requestId. */
export interface PendingStart {
	requestId: string;
	name: string;
	value: unknown;
	seconds: number | undefined;
}

/**
 * The start state machine, so it can be tested: one requestId per user
 * action; an unknown outcome is kept under that id and BLOCKS a fresh start
 * of the same tag (a new id could make two apply); a retry resends the same
 * id; a definite answer, a dismissal or seeing the entry in a re-read
 * clears it.
 */
export class SubstituteSession {
	readonly #client: SubstituteClient;
	readonly #pending = new Map<string, PendingStart>();

	constructor(client: SubstituteClient) {
		this.#client = client;
	}

	pending(): PendingStart[] {
		return [...this.#pending.values()];
	}

	/** Start afresh: a new requestId, unless this tag has an unknown start pending. */
	async start(name: string, value: unknown, seconds: number | undefined): Promise<SubstituteResult> {
		const key = name.toLowerCase();
		if (this.#pending.has(key)) {
			return {
				ok: false,
				kind: 'pending',
				message: `an earlier start of ${name} has an unknown outcome: re-read, retry that same request, or dismiss it`,
				retrySameId: false,
				maybeApplied: true
			};
		}
		return this.#send({ requestId: this.#client.newRequestId(), name, value, seconds });
	}

	/** Resend a pending start under ITS requestId. */
	async retry(name: string): Promise<SubstituteResult | null> {
		const p = this.#pending.get(name.toLowerCase());
		return p ? this.#send(p) : null;
	}

	/** Drop a pending start (the operator has checked the list). */
	dismiss(name: string): void {
		this.#pending.delete(name.toLowerCase());
	}

	/**
	 * A re-read that shows the entry means the start did apply: clear it.
	 * One that shows it LAPSED means it never will (a redundant pair
	 * committed it too late): clear it too, and return those, so the
	 * operator is told the start did not happen.
	 */
	reconcile(entries: SubstituteEntry[], lapsed: SubstituteLapse[] = []): PendingStart[] {
		for (const e of entries) {
			for (const [k, p] of this.#pending) if (e.requestId === p.requestId) this.#pending.delete(k);
		}
		const gone: PendingStart[] = [];
		for (const l of lapsed) {
			for (const [k, p] of this.#pending) {
				if (l.requestId && l.requestId === p.requestId) {
					this.#pending.delete(k);
					gone.push(p);
				}
			}
		}
		return gone;
	}

	async #send(p: PendingStart): Promise<SubstituteResult> {
		const r = await this.#client.start(p.name, p.value, p.seconds, p.requestId);
		const key = p.name.toLowerCase();
		// A 401/403 on a RETRY of an unknown start refuses that retry only
		// (who asked, this time: a sign-in ended, a role changed). It says
		// nothing of the first attempt, which may still take effect, so the
		// pending entry stays; its reason is the answer shown (#708).
		const retryRefusedByAuth = !r.ok && r.kind === 'forbidden' && this.#pending.get(key)?.requestId === p.requestId;
		if (!r.ok && (r.retrySameId || retryRefusedByAuth)) this.#pending.set(key, p);
		else this.#pending.delete(key);
		return r;
	}
}
