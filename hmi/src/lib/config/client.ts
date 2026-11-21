// The client for the config screens' two reads (#711): a schema slice per
// manifest section, and the manifest text. Every refusal says what to do
// about it. Pure (no DOM, no runes); the calls go through `kitFetch`, so
// sign-in and the elevation prompt apply as everywhere in the kit.

import { kitFetch } from '../kitfetch.js';
import type { Schema } from './schema.js';

/** GET /api/config/files. */
export interface ConfigFiles {
	/** False on a standby: the leader's configuration is the one in force. */
	leading: boolean;
	/** The manifest's file name. */
	manifest: string;
	/** The manifest and the files its drivers name, path to text. */
	files: Record<string, string>;
	/** The paths whose text is a live apply's, not the boot copy's. */
	differs?: string[];
}

export type ConfigFailure = 'signin' | 'forbidden' | 'unavailable' | 'standby' | 'noSection' | 'failed' | 'unreachable';

export type ConfigResult<T> = { ok: true; value: T } | { ok: false; kind: ConfigFailure; status: number; message: string };

/** The message for a failure: what happened, and what to do. */
export function failureText(kind: ConfigFailure, detail = ''): string {
	switch (kind) {
		case 'signin':
			return 'Sign in to edit the configuration.';
		case 'forbidden':
			return 'Your account may not edit the configuration. Ask for the write permission, or set server.online-edits: true in nautilus.yaml if edits are switched off on this controller.';
		case 'unavailable':
			return 'This controller does not serve its configuration. It needs live I/O configuration (a controller run by naut run) and a build with the config routes.';
		case 'standby':
			return 'This is a standby controller. Open the leader: its configuration is the one in force.';
		case 'noSection':
			return detail || 'The controller has no such configuration section.';
		case 'unreachable':
			return `No answer from the controller${detail ? `: ${detail}` : ''}. Check that it is running and reachable.`;
		default:
			return detail || 'The controller could not answer the request.';
	}
}

export function classify(status: number, body: string): { kind: ConfigFailure; message: string } {
	let message = body.trim();
	try {
		const j = JSON.parse(body) as { error?: string };
		if (j.error) message = j.error;
	} catch {
		/* not JSON: the text stands */
	}
	if (status === 401) return { kind: 'signin', message: failureText('signin') };
	if (status === 403) return { kind: 'forbidden', message: failureText('forbidden') };
	if (status === 404) return { kind: 'noSection', message: failureText('noSection', message) };
	if (status === 503) return { kind: 'unavailable', message: failureText('unavailable') };
	return { kind: 'failed', message: failureText('failed', message) };
}

export interface ConfigClientOptions {
	/** Default `/api/config`. */
	url?: string;
	token?: string;
	/** Injectable for tests. */
	fetch?: typeof fetch;
}

export class ConfigClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: typeof fetch;

	constructor(opts: ConfigClientOptions = {}) {
		this.#url = opts.url ?? '/api/config';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	async #get<T>(path: string, standbyCheck: (v: T) => boolean = () => false): Promise<ConfigResult<T>> {
		const headers: Record<string, string> = {};
		if (this.#token) headers['X-Nautilus-Token'] = this.#token;
		let res: Response;
		try {
			res = await this.#fetch(`${this.#url}${path}`, { headers });
		} catch (e) {
			const message = e instanceof Error ? e.message : String(e);
			return { ok: false, kind: 'unreachable', status: 0, message: failureText('unreachable', message) };
		}
		const body = await res.text();
		if (!res.ok) return { ok: false, ...classify(res.status, body), status: res.status };
		let value: T;
		try {
			value = JSON.parse(body) as T;
		} catch {
			return { ok: false, kind: 'failed', status: res.status, message: failureText('failed', 'The controller answered with something that is not JSON.') };
		}
		if (standbyCheck(value)) return { ok: false, kind: 'standby', status: res.status, message: failureText('standby') };
		return { ok: true, value };
	}

	/** The sections the schema describes. */
	sections(): Promise<ConfigResult<string[]>> {
		return this.#get<{ sections: string[] }>('/schema').then((r) => (r.ok ? { ok: true as const, value: r.value.sections } : r));
	}

	/** One section as a self-contained JSON Schema. */
	schema(section: string): Promise<ConfigResult<Schema>> {
		return this.#get<Schema>(`/schema?section=${encodeURIComponent(section)}`);
	}

	/** The manifest text, as what runs. A standby is a failure, not text. */
	files(): Promise<ConfigResult<ConfigFiles>> {
		return this.#get<ConfigFiles>('/files', (f) => f.leading === false);
	}
}
