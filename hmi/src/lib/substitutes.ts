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

export interface SubstituteEntry {
	id: string;
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

export interface SubstituteList {
	substitutes: SubstituteEntry[];
	rules: SubstituteRule[];
}

/** Why an action did not (certainly) happen. */
export type SubstituteFailure =
	| 'invalid' // 400: the request is wrong
	| 'forbidden' // 403 (e.g. an operator value the rule does not allow)
	| 'notFound' // 404: not active (already ended)
	| 'conflict' // 409: reused requestId, superseded, or nothing to ack — re-read, decide again
	| 'noLeader' // 503: standby / no leader — nothing was started
	| 'notSaved' // 503: the retain store refused — nothing was started
	| 'unknown' // 503 outcome unknown — it MAY still take effect: re-read
	| 'unreachable'; // network: no answer at all, the same as unknown

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

/** Map a non-2xx answer to the contract's failure kinds. Exported for tests. */
export function classify(status: number, text: string, outcomeHeader: string | null): { kind: SubstituteFailure; message: string } {
	const message = text.trim() || `request failed (${status})`;
	const lower = message.toLowerCase();
	if (status === 503) {
		if ((outcomeHeader ?? '').toLowerCase() === 'unknown' || lower.includes('outcome unknown')) {
			return { kind: 'unknown', message };
		}
		if (lower.includes('no leader')) return { kind: 'noLeader', message };
		return { kind: 'notSaved', message };
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
		this.#fetch = opts.fetch ?? ((...a) => fetch(...a));
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
			if (!res.ok) return { error: (await res.text()).trim() || `read failed (${res.status})` };
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
	stop(id: string): Promise<SubstituteResult> {
		return this.#send(`${this.#url}/${encodeURIComponent(id)}`, 'DELETE', undefined, undefined);
	}

	/** Acknowledge substitutes started with ackOnStart. */
	ack(ids: string[]): Promise<SubstituteResult> {
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
				retrySameId: true,
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
			// Only a lost/unknown answer is retried under the same id. A 409 or
			// 503-not-started must be re-read and decided again.
			retrySameId: kind === 'unknown',
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
			return `Outcome unknown — it may still take effect. Re-read the list before trying again. (${r.message})`;
		case 'conflict':
			return `Not applied — another change got there first. Re-read and decide again. (${r.message})`;
		case 'noLeader':
			return `Nothing was started — this controller has no leader to take the change. (${r.message})`;
		case 'notSaved':
			return `Nothing was started — the change could not be saved. (${r.message})`;
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
	if (rule.operatorValue && (value === undefined || value === '' || value === null)) return 'Enter the value to hold.';
	return null;
}
