// The client for #706's staged-apply routes, as the config screens' review
// panel drives them (#711, slice 3): stage the edited files, validate, apply.
// Every refusal names the fix. Calls go through `kitFetch`, so sign-in and
// the elevation prompt apply as everywhere in the kit.

import { kitFetch } from '../kitfetch.js';
import { classify, failureText, type ConfigResult } from './client.js';

export interface Check { name: string; ok: boolean; skipped?: boolean; detail?: string }
export interface Probe { driver: string; device?: string; mode: string; why: string; ok: boolean; detail?: string; bad?: string[] }
export interface Change { driver: string; device?: string; before: string; after: string }
export interface Command { tag: string; value: unknown }
export interface Validation { valid: boolean; checks: Check[]; changes: Change[]; willCommand: Command[]; held?: boolean; notes?: string[]; probes?: Probe[] }
export interface Applied { changes: Change[]; answered?: string[]; pending?: string[]; commanded: Command[]; notes?: string[]; kept: boolean; keptWhy?: string; watch?: Watch }
export interface Watch { state: string; pending?: string[]; answered?: string[]; silent?: string[]; revert: boolean; reason?: string }

/** A staged candidate, as /api/io/staged answers. */
export interface Staged {
	id: string;
	state: 'pending' | 'valid' | 'invalid' | 'applying' | 'applied' | 'failed';
	error?: string;
	validation?: Validation;
	result?: Applied;
}

/** Why a step was refused, with the fix. */
export function refusal(status: number, body: string): { kind: ReturnType<typeof classify>['kind']; message: string } {
	if (status === 409) {
		let m = body;
		try { m = (JSON.parse(body) as { error?: string }).error ?? body; } catch { /* text */ }
		return { kind: 'failed', message: `${m.trim()} Validate the candidate again, or stage the edit again if the controller restarted.` };
	}
	if (status === 422) {
		let m = body;
		try { m = (JSON.parse(body) as { error?: string }).error ?? body; } catch { /* text */ }
		return { kind: 'failed', message: `The controller refused the edit: ${m.trim()} Fix it in the form and review again.` };
	}
	return classify(status, body);
}

export interface ReviewClientOptions { url?: string; token?: string; fetch?: typeof fetch }

export class ReviewClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: typeof fetch;

	constructor(opts: ReviewClientOptions = {}) {
		this.#url = opts.url ?? '/api/io';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	async #post(path: string, body?: unknown): Promise<ConfigResult<Staged>> {
		const headers: Record<string, string> = {};
		if (this.#token) headers['X-Nautilus-Token'] = this.#token;
		if (body !== undefined) headers['Content-Type'] = 'application/json';
		let res: Response;
		try {
			res = await this.#fetch(`${this.#url}${path}`, { method: 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body) });
		} catch (e) {
			return { ok: false, kind: 'unreachable', status: 0, message: failureText('unreachable', e instanceof Error ? e.message : String(e)) };
		}
		const text = await res.text();
		// 500/503 from apply still carry the candidate (state failed + error).
		let value: Staged | undefined;
		try { const j = JSON.parse(text) as Staged; if (j && typeof j.id === 'string') value = j; } catch { /* error body */ }
		if (res.ok && value) return { ok: true, value };
		if (value?.error) return { ok: false, kind: 'failed', status: res.status, message: value.error };
		return { ok: false, ...refusal(res.status, text), status: res.status };
	}

	/** Stage the changed files over what the controller runs. */
	stage(files: Record<string, string>): Promise<ConfigResult<Staged>> {
		return this.#post('/staged', { files });
	}

	validate(id: string): Promise<ConfigResult<Staged>> {
		return this.#post(`/staged/${encodeURIComponent(id)}/validate`);
	}

	/** Apply a valid candidate; revertIfSilent is a duration like "10s". */
	apply(id: string, revertIfSilent?: string): Promise<ConfigResult<Staged>> {
		return this.#post(`/staged/${encodeURIComponent(id)}/apply`, revertIfSilent ? { revertIfSilent } : undefined);
	}
}
