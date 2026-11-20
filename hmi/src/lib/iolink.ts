// IO-Link device parameters (#705): the client for /api/iolink/{port}/params,
// and the pure rules the panel renders from. No runes, no DOM — tested in
// tests/iolink.test.ts with a fake fetch, because the contract it honours is
// a device-write one:
//
//   - a read works whichever device is on the port and says which: a value
//     from a device that is not the one expected is shown as such, never as
//     the device's;
//   - a write the controller REFUSES (409: the device on the port is not the
//     one expected; 422: the IODD forbids it) changed nothing, and says why;
//   - a write whose outcome is UNCERTAIN (the device on the port changed
//     during it; any 5xx; the connection lost mid-write) is NOT a failure and
//     NOT a success: the parameter may or may not have been written, to the
//     right device or the wrong one. The caller must read it back and say so.

import { kitFetch } from './kitfetch.js';
/** A named value of an enumerated parameter: "1" is "Auto". */
export interface ParamValueName {
	value: string;
	name: string;
}

/** One parameter of a port device's IODD (a record item is listed on its own). */
export interface ParamInfo {
	id: string;
	name?: string;
	index: number;
	/** What read and write take for it. */
	ref: string;
	type?: string;
	/** "ro", "wo" or "rw". */
	access: string;
	octets?: number;
	/** The IODD's allowed ranges, "0..1000". */
	range?: string[];
	default?: string;
	values?: ParamValueName[];
	/** The port's master can carry it in one call. */
	carried: boolean;
}

export interface ParamList {
	port: string;
	master: string;
	params: ParamInfo[];
}

/** A parameter as read or written, with the port's identity at the time. */
export interface ParamAnswer {
	port: string;
	variable: string;
	index: number;
	subindex: number;
	value: unknown;
	/** The octets on the wire, as hex. */
	raw: string;
	/** Ok, NoDevice, WrongDevice or Unchecked. */
	identity: string;
	identityChecked: boolean;
}

export type ParamFailure =
	/** Refused before anything changed: the device on the port is not the one expected, or the master cannot carry it. */
	| 'refused'
	/** The IODD forbids it: a value out of range, a read-only variable. */
	| 'invalid'
	| 'notFound'
	| 'forbidden'
	/** Another ISDU call is in flight for the port; nothing was sent: try again. */
	| 'busy'
	/** The outcome is not known: the parameter may or may not have been written. */
	| 'uncertain'
	/** A read that did not get an answer (a write that did not is 'uncertain'). */
	| 'unreachable'
	/** The master or the device answered with a failure of its own. */
	| 'failed';

export type ParamResult<T> =
	| { ok: true; value: T }
	| { ok: false; kind: ParamFailure; status: number; message: string };

export interface PortParamClientOptions {
	/** Default `/api/iolink`. */
	url?: string;
	token?: string;
	/** Injectable for tests. */
	fetch?: typeof fetch;
}

export const OUTCOME_HEADER = 'X-Nautilus-Outcome';

/**
 * Map a non-2xx answer to the contract's failure kinds. Only an answer the
 * controller marked `refused` (or a 4xx the controller gave) is certain to
 * have changed nothing; an `outcome: unknown` body or header, a 502 without a
 * refusal, and every other 5xx can come after the device took the value.
 * Exported for tests.
 */
export function classify(status: number, body: string, outcomeHeader: string | null, write: boolean): { kind: ParamFailure; message: string } {
	let message = body.trim() || `request failed (${status})`;
	let outcome = (outcomeHeader ?? '').toLowerCase();
	try {
		const j = JSON.parse(body) as { error?: string; outcome?: string };
		if (j.error) message = j.error;
		if (j.outcome) outcome = j.outcome.toLowerCase();
	} catch {
		/* not JSON: the text stands */
	}
	if (outcome === 'unknown') return { kind: write ? 'uncertain' : 'failed', message };
	if (status === 401 || status === 403) return { kind: 'forbidden', message };
	if (status === 429) return { kind: 'busy', message };
	if (status === 404) return { kind: 'notFound', message };
	if (status === 409) return { kind: 'refused', message };
	if (status === 422 || status === 400) return { kind: 'invalid', message };
	if (status >= 500) return { kind: write ? 'uncertain' : 'failed', message };
	return { kind: 'failed', message };
}

export class PortParamClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: typeof fetch;

	constructor(opts: PortParamClientOptions = {}) {
		this.#url = opts.url ?? '/api/iolink';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	#headers(json: boolean): Record<string, string> {
		const h: Record<string, string> = {};
		if (json) h['Content-Type'] = 'application/json';
		if (this.#token) h['X-Nautilus-Token'] = this.#token;
		return h;
	}

	#path(port: string, ref?: string): string {
		const base = `${this.#url}/${encodeURIComponent(port)}/params`;
		return ref === undefined ? base : `${base}/${encodeURIComponent(ref)}`;
	}

	async #send<T>(url: string, init: RequestInit, write: boolean): Promise<ParamResult<T>> {
		let res: Response;
		try {
			res = await this.#fetch(url, init);
		} catch {
			// A write that may have been sent is uncertain; a read just failed.
			return { ok: false, kind: write ? 'uncertain' : 'unreachable', status: 0, message: write ? 'the connection was lost during the write' : 'controller unreachable' };
		}
		const text = await res.text();
		if (!res.ok) {
			const c = classify(res.status, text, res.headers.get(OUTCOME_HEADER), write);
			return { ok: false, kind: c.kind, status: res.status, message: c.message };
		}
		try {
			return { ok: true, value: JSON.parse(text) as T };
		} catch {
			return { ok: false, kind: write ? 'uncertain' : 'failed', status: res.status, message: 'the controller answered something that is not JSON' };
		}
	}

	/** A port device's parameters from its IODD (no traffic to the device). */
	list(port: string): Promise<ParamResult<ParamList>> {
		return this.#send<ParamList>(this.#path(port), { headers: this.#headers(false) }, false);
	}

	/** Read one parameter, whichever device is on the port. */
	read(port: string, ref: string): Promise<ParamResult<ParamAnswer>> {
		return this.#send<ParamAnswer>(this.#path(port, ref), { headers: this.#headers(false) }, false);
	}

	/** Write one. An uncertain outcome is its own failure kind: read it back. */
	write(port: string, ref: string, value: unknown): Promise<ParamResult<ParamAnswer>> {
		return this.#send<ParamAnswer>(this.#path(port, ref), { method: 'PUT', headers: this.#headers(true), body: JSON.stringify({ value }) }, true);
	}
}

/** The parameter can be read from the panel. */
export function canRead(p: ParamInfo): boolean {
	return p.access !== 'wo' && p.carried;
}

/** The parameter can be written from the panel (the IODD allows it, the master carries it). */
export function canWrite(p: ParamInfo): boolean {
	return p.access !== 'ro' && p.carried;
}

/** A value for display: its name beside it where the IODD names it. */
export function valueLabel(p: ParamInfo, value: unknown): string {
	const text = value === null || value === undefined ? '' : String(value);
	const named = p.values?.find((v) => v.value === text)?.name;
	return named ? `${named} (${text})` : text;
}

/** How much to trust a read: ok (the right device), warn (not known), bad (the wrong one). */
export function identityTone(identity: string, checked: boolean): 'ok' | 'warn' | 'bad' {
	if (!checked) return 'warn';
	if (identity === 'Ok') return 'ok';
	if (identity === 'Unchecked') return 'warn';
	return 'bad';
}

/** The line shown with a read, saying which device answered. */
export function identityText(identity: string, checked: boolean): string {
	if (!checked) return 'The master has not reported its ids: not known which device answered.';
	switch (identity) {
		case 'Ok':
			return 'The device on the port is the one expected.';
		case 'Unchecked':
			return 'This master reports no ids: the device on the port cannot be checked.';
		case 'NoDevice':
			return 'No device is on the port: this is not the device\'s value.';
		case 'WrongDevice':
			return 'The device on the port is NOT the one expected: this is what that device holds.';
	}
	return `Identity: ${identity}.`;
}

/** A value typed into the panel: a number, true/false, or text. */
export function parseInput(p: ParamInfo, text: string): { ok: true; value: unknown } | { ok: false; error: string } {
	const s = text.trim();
	if (s === '') return { ok: false, error: 'Enter a value.' };
	if (p.values?.length) {
		const byName = p.values.find((v) => v.name.toLowerCase() === s.toLowerCase());
		if (byName) return { ok: true, value: Number(byName.value) };
	}
	if (s === 'true') return { ok: true, value: true };
	if (s === 'false') return { ok: true, value: false };
	if (/^-?\d+$/.test(s)) {
		const n = Number(s);
		if (Number.isSafeInteger(n)) return { ok: true, value: n };
		return { ok: false, error: 'That integer is beyond what the panel can send exactly; use the CLI (naut iolink param set).' };
	}
	const f = Number(s);
	if (Number.isFinite(f)) return { ok: true, value: f };
	if ((p.type ?? '').startsWith('String') || (p.type ?? '').startsWith('OctetString')) return { ok: true, value: s };
	return { ok: false, error: `${p.id} takes a number${p.values?.length ? ' or one of its names' : ''}.` };
}

/** What a failed action says. An uncertain outcome never says "failed". */
export function failureText(kind: ParamFailure, message: string): string {
	switch (kind) {
		case 'uncertain':
			return `The outcome is not known: ${message}. The parameter may or may not have been written, and the wrong device may have taken it. Read it back before writing again.`;
		case 'refused':
			return `Refused, nothing was written: ${message}`;
		case 'invalid':
			return `Not allowed by the IODD, nothing was written: ${message}`;
		case 'forbidden':
			return `Not permitted: ${message}`;
		case 'notFound':
			return `Not found: ${message}`;
		case 'busy':
			return 'Another parameter call is in flight for this port, and nothing was sent. Try again in a moment.';
		case 'unreachable':
			return 'The controller is unreachable.';
	}
	return `Failed: ${message}`;
}
