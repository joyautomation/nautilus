// The client for #706's staged-apply routes, as the config screens' review
// panel drives them (#711, slice 3): stage the edited files, validate, apply.
// Every refusal names the fix. Calls go through `kitFetch`, so sign-in and
// the elevation prompt apply as everywhere in the kit.

import { kitFetch } from '../kitfetch.js';
import { classify, failureText, type ConfigFailure } from './client.js';

export interface Check { name: string; ok: boolean; skipped?: boolean; detail?: string }
export interface Probe { driver: string; device?: string; mode: string; why: string; ok: boolean; detail?: string; bad?: string[] }
export interface Change { driver: string; device?: string; before: string; after: string }
export interface Command { tag: string; value: unknown }
export interface Validation { valid: boolean; checks: Check[]; changes: Change[]; willCommand: Command[]; held?: boolean; notes?: string[]; probes?: Probe[]; needsRestart?: boolean }

/** A candidate saved (committed) to apply at the next restart: who, when, and the commit. */
export interface Saved { commit: string; branch: string; files: string[]; by: string; at: string; pending: boolean }
export interface Applied { changes: Change[]; answered?: string[]; pending?: string[]; commanded: Command[]; notes?: string[]; kept: boolean; keptWhy?: string; watch?: Watch }
export interface Watch { state: string; pending?: string[]; answered?: string[]; silent?: string[]; revert: boolean; reason?: string }

/** A staged candidate, as /api/io/staged answers. */
export interface Staged {
	id: string;
	state: 'pending' | 'valid' | 'invalid' | 'applying' | 'applied' | 'failed';
	error?: string;
	validation?: Validation;
	result?: Applied;
	/** Set once saved for the next restart (POST /api/config/save). */
	saved?: Saved;
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

/** A step's answer; a failure that moved the candidate carries it. */
export type ReviewResult = { ok: true; value: Staged } | { ok: false; kind: ConfigFailure; status: number; message: string; staged?: Staged };

/** The commit POST /api/config/commit made. */
export interface Committed { commit: string; branch: string; files: string[] }

/** What a restart costs, in the plain words the screen shows beside a save for restart. */
export const RESTART_NOTE =
	'The scan stops while the controller restarts, and it starts again from the committed files. On a redundant pair, restart the standby first, then switch over, then restart the former leader.';

/** The one line that says a change is saved and waiting: who saved it, which commit, and that nothing running changed. */
export function savedLine(s: Saved): string {
	return `Saved for the next restart by ${s.by}: commit ${s.commit.slice(0, 12)} on ${s.branch} (${s.files.join(', ')}). Nothing running has changed; it applies when the controller restarts.`;
}

export type SaveResult = { ok: true; value: Saved } | { ok: false; kind: ConfigFailure; status: number; message: string };

/** `patch` is set when the refusal names the patch route as the way out (no checkout). */
export type CommitResult = { ok: true; value: Committed } | { ok: false; kind: ConfigFailure; status: number; message: string; patch?: boolean };
export type PatchResult = { ok: true; patch: string } | { ok: false; kind: ConfigFailure; status: number; message: string };

export interface ReviewClientOptions { url?: string; configUrl?: string; token?: string; fetch?: typeof fetch }

export class ReviewClient {
	readonly #url: string;
	readonly #configUrl: string;
	readonly #token?: string;
	readonly #fetch: typeof fetch;

	constructor(opts: ReviewClientOptions = {}) {
		this.#url = opts.url ?? '/api/io';
		this.#configUrl = opts.configUrl ?? '/api/config';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	async #post(path: string, body?: unknown): Promise<ReviewResult> {
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
		const r = refusal(res.status, value?.error ? JSON.stringify({ error: value.error }) : text);
		return { ok: false, ...r, status: res.status, staged: value };
	}

	/** Stage the changed files over what the controller runs. */
	stage(files: Record<string, string>): Promise<ReviewResult> {
		return this.#post('/staged', { files });
	}

	validate(id: string): Promise<ReviewResult> {
		return this.#post(`/staged/${encodeURIComponent(id)}/validate`);
	}

	/** Apply a valid candidate; revertIfSilent is a duration like "10s". */
	apply(id: string, revertIfSilent?: string): Promise<ReviewResult> {
		return this.#post(`/staged/${encodeURIComponent(id)}/apply`, revertIfSilent ? { revertIfSilent } : undefined);
	}

	async #config(method: 'GET' | 'POST', path: string, body?: unknown): Promise<{ ok: true; text: string } | { ok: false; kind: ConfigFailure; status: number; message: string }> {
		const headers: Record<string, string> = {};
		if (this.#token) headers['X-Nautilus-Token'] = this.#token;
		if (body !== undefined) headers['Content-Type'] = 'application/json';
		let res: Response;
		try {
			res = await this.#fetch(`${this.#configUrl}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
		} catch (e) {
			return { ok: false, kind: 'unreachable', status: 0, message: failureText('unreachable', e instanceof Error ? e.message : String(e)) };
		}
		const text = await res.text();
		if (res.ok) return { ok: true, text };
		if (res.status === 404 && method === 'POST') {
			let m = text;
			try { m = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* text */ }
			return { ok: false, kind: 'failed', status: 404, message: `${m.trim()} (the controller may have restarted). Stage, validate and apply the change again, then commit.` };
		}
		if (res.status === 409) {
			// The server's text already names the way out (the patch, the
			// candidate to commit); pass it through whole.
			let m = text;
			try { m = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* text */ }
			return { ok: false, kind: 'failed', status: 409, message: m.trim() };
		}
		return { ok: false, ...classify(res.status, text), status: res.status };
	}

	/** Save the applied candidate's change as a git commit (only where the controller runs from a checkout). */
	async commit(id: string, message?: string): Promise<CommitResult> {
		const r = await this.#config('POST', '/commit', message ? { id, message } : { id });
		if (!r.ok) return { ...r, patch: r.status === 409 && r.message.includes('/api/config/patch') };
		try { return { ok: true, value: JSON.parse(r.text) as Committed }; } catch { return { ok: false, kind: 'failed', status: 200, message: 'The controller answered the commit with something unreadable.' }; }
	}

	/**
	 * Save a candidate that loads but needs a restart (a device added or removed) as a
	 * git commit in the signed-in user's name. Nothing running changes.
	 */
	async save(id: string, message?: string): Promise<SaveResult> {
		const r = await this.#config('POST', '/save', message ? { id, message } : { id });
		if (!r.ok) return r;
		try { return { ok: true, value: JSON.parse(r.text) as Saved }; } catch { return { ok: false, kind: 'failed', status: 200, message: 'The controller answered the save with something unreadable.' }; }
	}

	/** What runs, where it differs from the project, as a patch to apply in your repo. */
	async patch(): Promise<PatchResult> {
		const r = await this.#config('GET', '/patch');
		return r.ok ? { ok: true, patch: r.text } : r;
	}
}
