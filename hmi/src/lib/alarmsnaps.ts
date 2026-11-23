// The pictures kept when an alarm activates (#724 slice 2), as the HMI kit asks
// for them: GET /api/camera-snapshots?occurrence=<id>. Plain TS (no runes, no
// DOM), tested in tests/alarmsnaps.test.ts.
//
// A controller that keeps no pictures answers 404 (or fails): the client
// remembers that and asks no more, so a journal of a hundred rows costs one
// failed request, not a hundred. A 403 means the person holds read but not the
// `camera` permission: also remembered, and said once, not per row.

import { kitFetch, type Fetch } from './kitfetch.js';

export interface AlarmSnap {
	file: string;
	/** Epoch ms. */
	ts: number;
	camera: string;
	alarm: string;
	/** `/api/camera-snapshots/<file>` (image/jpeg, never cached). */
	url: string;
}

export type SnapState = 'none' | 'denied' | 'ok';

export interface SnapResult {
	state: SnapState;
	snaps: AlarmSnap[];
}

export interface SnapshotClientOptions {
	/** Default `/api/camera-snapshots`. */
	url?: string;
	token?: string;
	fetch?: Fetch;
	/** Requests in flight at once. Default 3. */
	concurrency?: number;
}

export const NO_SNAP_VIEW = 'Alarm pictures: no permission to view cameras.';
export const MAX_THUMBS = 3;

const NONE: SnapResult = { state: 'none', snaps: [] };
const DENIED: SnapResult = { state: 'denied', snaps: [] };

function normalize(j: any): AlarmSnap | null {
	// only a path on this controller is a picture: never follow another origin
	if (!j || typeof j.url !== 'string' || !j.url.startsWith('/') || j.url.startsWith('//')) return null;
	return {
		file: typeof j.file === 'string' ? j.file : '',
		ts: typeof j.ts === 'number' && isFinite(j.ts) ? j.ts : 0,
		camera: typeof j.camera === 'string' ? j.camera : '',
		alarm: typeof j.alarm === 'string' ? j.alarm : '',
		url: j.url
	};
}

/** The pictures of a response body; anything unreadable is none. */
export function parseSnaps(j: any): AlarmSnap[] {
	if (!Array.isArray(j?.snapshots)) return [];
	return (j.snapshots as unknown[]).map(normalize).filter((s): s is AlarmSnap => s !== null);
}

/** The thumbnails to draw and how many more there are ("+N"). */
export function thumbSplit<T>(snaps: T[], max = MAX_THUMBS): { shown: T[]; more: number } {
	return { shown: snaps.slice(0, max), more: Math.max(0, snaps.length - max) };
}

export class SnapshotClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;
	readonly #limit: number;
	#none = false;
	#denied = false;
	#active = 0;
	readonly #waiting: Array<() => void> = [];
	readonly #cache = new Map<string, Promise<SnapResult>>();

	constructor(opts: SnapshotClientOptions = {}) {
		this.#url = opts.url ?? '/api/camera-snapshots';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
		this.#limit = Math.max(1, opts.concurrency ?? 3);
	}

	/** The controller answered 404 (or failed): it keeps no pictures; no more is asked. */
	get keepsNone(): boolean {
		return this.#none;
	}

	/** The controller answered 403: the person may not view cameras; no more is asked. */
	get denied(): boolean {
		return this.#denied;
	}

	list(occurrence: string): Promise<SnapResult> {
		if (this.#none) return Promise.resolve(NONE);
		if (this.#denied) return Promise.resolve(DENIED);
		let p = this.#cache.get(occurrence);
		if (!p) {
			p = this.#queued(occurrence);
			this.#cache.set(occurrence, p);
		}
		return p;
	}

	async #queued(occurrence: string): Promise<SnapResult> {
		if (this.#active >= this.#limit) await new Promise<void>((r) => this.#waiting.push(r));
		else this.#active++;
		try {
			// a 404 or 403 that came back while this one waited settles it
			if (this.#none) return NONE;
			if (this.#denied) return DENIED;
			return await this.#get(occurrence);
		} finally {
			const next = this.#waiting.shift();
			if (next) next();
			else this.#active--;
		}
	}

	async #get(occurrence: string): Promise<SnapResult> {
		try {
			const res = await this.#fetch(`${this.#url}?occurrence=${encodeURIComponent(occurrence)}`, {
				headers: this.#token ? { 'X-Nautilus-Token': this.#token } : {}
			});
			if (res.status === 403) {
				this.#denied = true;
				return DENIED;
			}
			if (res.status === 404) {
				this.#none = true;
				return NONE;
			}
			if (!res.ok) return NONE;
			const snaps = parseSnaps(await res.json());
			return snaps.length ? { state: 'ok', snaps } : NONE;
		} catch {
			return NONE;
		}
	}
}

/** One client for every journal that does not bring its own. */
export const defaultSnapshotClient = new SnapshotClient();
