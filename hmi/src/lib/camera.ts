// Built-in cameras (#724), as the HMI kit shows them: the client for
// /api/cameras and the rules the panel renders from. Plain TS (no runes, no
// DOM), tested in tests/camera.test.ts.
//
// The panel's rule is that live video costs bandwidth, and over a slow link
// (radio, cellular) it can slow the tag values, so a still is the default and
// live starts only on an explicit click; it stops when the tab is hidden, when
// the person says stop, when the page goes away and when the stream fails.
// A controller with no cameras (the route is not mounted, an empty list, no
// answer) shows nothing at all, and no error.

import { kitFetch, type Fetch } from './kitfetch.js';

export interface Camera {
	id: string;
	label: string;
	/** The plant equipment id this camera looks at. */
	asset: string;
	kind: string;
	/** Seconds between snapshot refreshes. */
	interval: number;
	maxViewers: number;
	/** The person may view cameras (the controller's `camera` permission); false: say so, ask for no picture. */
	view: boolean;
	/** A live stream exists for this camera kind. */
	live: boolean;
	/** The live stream is HLS (an RTSP camera): play it with `playHls` in a <video>, not as an <img>. */
	hls: boolean;
	/** The camera pans, tilts and zooms (ONVIF): show the pad to a person who holds the `ptz` permission. */
	ptz: boolean;
	/** A still exists. */
	snapshot: boolean;
	/** How many are watching live now. */
	watching: number;
	/** Bits per second the relay carries (0 when nobody watches). */
	bps: number;
}

export interface CameraClientOptions {
	/** Default `/api/cameras`. */
	url?: string;
	token?: string;
	fetch?: Fetch;
}

/** What was said about a camera URL that would not show: the status and the controller's own words. */
export interface Explained {
	status: number;
	message: string;
}

const GENERIC = 'the camera did not answer';
export const NO_VIEW = 'No permission to view cameras.';

export const DEFAULT_INTERVAL = 5;

function normalize(j: any): Camera | null {
	if (!j || typeof j.id !== 'string' || !j.id) return null;
	const n = (v: unknown, d: number) => (typeof v === 'number' && isFinite(v) ? v : d);
	return {
		id: j.id,
		label: typeof j.label === 'string' && j.label ? j.label : j.id,
		asset: typeof j.asset === 'string' ? j.asset : '',
		kind: typeof j.kind === 'string' ? j.kind : '',
		interval: n(j.interval, DEFAULT_INTERVAL) > 0 ? n(j.interval, DEFAULT_INTERVAL) : DEFAULT_INTERVAL,
		maxViewers: n(j.maxViewers, 0),
		// an older controller says nothing: it has no such permission
		view: j.view !== false,
		live: j.live === true,
		hls: j.hls === true,
		ptz: j.ptz === true,
		snapshot: j.snapshot === true,
		watching: n(j.watching, 0),
		bps: n(j.bps, 0)
	};
}

export class CameraClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: CameraClientOptions = {}) {
		this.#url = opts.url ?? '/api/cameras';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	#headers(): Record<string, string> {
		return this.#token ? { 'X-Nautilus-Token': this.#token } : {};
	}

	/** The controller's cameras. A route not mounted (404), a failure or a bad body is no cameras, never an error. */
	async list(): Promise<Camera[]> {
		try {
			const res = await this.#fetch(this.#url, { headers: this.#headers() });
			if (!res.ok) return [];
			const j: any = await res.json();
			if (!Array.isArray(j?.cameras)) return [];
			return (j.cameras as unknown[]).map(normalize).filter((c): c is Camera => c !== null);
		} catch {
			return [];
		}
	}

	/** The still's URL; the nonce keeps the browser from showing an old one. */
	snapshotUrl(id: string, nonce: string | number = Date.now()): string {
		return `${this.#url}/${encodeURIComponent(id)}/snapshot?n=${encodeURIComponent(String(nonce))}`;
	}

	/** The motion JPEG stream's URL (an <img src>). */
	streamUrl(id: string): string {
		return `${this.#url}/${encodeURIComponent(id)}/stream`;
	}

	/** The directory of an HLS camera's playlist (for `playHls`). */
	hlsUrl(id: string): string {
		return `${this.#url}/${encodeURIComponent(id)}/hls`;
	}

	/** A camera's presets (the device's own, names cleaned by the controller); none on any failure. */
	async ptzPresets(id: string): Promise<PtzPreset[]> {
		try {
			const res = await this.#fetch(`${this.#url}/${encodeURIComponent(id)}/ptz`, { headers: this.#headers() });
			if (!res.ok) return [];
			const j: any = await res.json();
			if (!Array.isArray(j?.presets)) return [];
			return (j.presets as any[])
				.filter((p) => p && typeof p.token === 'string' && p.token)
				.map((p) => ({ token: p.token as string, name: typeof p.name === 'string' && p.name ? p.name : p.token }));
		} catch {
			return [];
		}
	}

	/**
	 * Move, stop or go to a preset. It goes through the kit's fetch, so an elevated
	 * permission asks for the password once and the move is sent again; the answer is
	 * the controller's own words when it refuses.
	 */
	async ptz(id: string, body: PtzCommand, signal?: AbortSignal): Promise<PtzResult> {
		return this.#post(`${this.#url}/${encodeURIComponent(id)}/ptz`, JSON.stringify(body), signal);
	}

	/**
	 * Stop a camera. A way back: it needs no ptz permission and no fresh password, and
	 * it is sent with `keepalive`, so it still goes when the page is closing.
	 */
	async ptzStop(id: string): Promise<PtzResult> {
		return this.#post(`${this.#url}/${encodeURIComponent(id)}/ptz/stop`, '', undefined, true);
	}

	async #post(url: string, body: string, signal?: AbortSignal, keepalive = false): Promise<PtzResult> {
		try {
			const res = await this.#fetch(url, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', ...this.#headers() },
				body: body || undefined,
				signal,
				keepalive
			});
			if (res.ok) return { ok: true, status: res.status, message: '' };
			let message = '';
			try {
				const j: any = JSON.parse(await res.text());
				if (j && typeof j.error === 'string') message = j.error;
			} catch {
				// not JSON
			}
			return { ok: false, status: res.status, message: message || `The camera did not answer (${res.status}).` };
		} catch {
			return { ok: false, status: 0, message: 'The controller did not answer.' };
		}
	}

	/**
	 * An <img> cannot show a JSON error body, so ask the same URL once and read
	 * it. The request is aborted so a stream that does open is not kept open.
	 */
	async describe(url: string): Promise<Explained> {
		const ctl = new AbortController();
		try {
			const res = await this.#fetch(url, { headers: this.#headers(), signal: ctl.signal });
			let message = '';
			if (!res.ok) {
				try {
					const j: any = JSON.parse(await res.text());
					if (j && typeof j.error === 'string') message = j.error;
				} catch {
					// not JSON: the generic message below
				}
			}
			return { status: res.status, message: message || (res.ok ? GENERIC : `${GENERIC} (${res.status})`) };
		} catch {
			return { status: 0, message: GENERIC };
		} finally {
			ctl.abort();
		}
	}

	/** The controller's own words for why this URL would not show. */
	async explain(url: string): Promise<string> {
		return (await this.describe(url)).message;
	}
}

export interface PtzPreset {
	token: string;
	name: string;
}

export type PtzCommand = { action: 'move'; x: number; y: number; zoom: number; ms: number } | { action: 'preset'; preset: string };

export interface PtzResult {
	ok: boolean;
	status: number;
	message: string;
}

/** How long a held button moves the camera at most (the controller's limit is 10 s); the browser sends a stop when it is let go. */
export const PTZ_HOLD_MS = 10_000;
/** A nudge: a key press, or a tap. */
export const PTZ_NUDGE_MS = 400;

export type PtzDirection = 'up' | 'down' | 'left' | 'right' | 'in' | 'out';

/** A direction as the controller's velocity (-1 to 1 for each of pan, tilt and zoom). */
export function ptzMove(dir: PtzDirection, ms: number): PtzCommand {
	const speed = 0.5;
	const v = { x: 0, y: 0, zoom: 0 };
	switch (dir) {
		case 'left':
			v.x = -speed;
			break;
		case 'right':
			v.x = speed;
			break;
		case 'up':
			v.y = speed;
			break;
		case 'down':
			v.y = -speed;
			break;
		case 'in':
			v.zoom = speed;
			break;
		case 'out':
			v.zoom = -speed;
			break;
	}
	return { action: 'move', ...v, ms: Math.max(100, Math.min(PTZ_HOLD_MS, Math.round(ms))) };
}

/** Bits per second, for people: 0, 1.5 kbit/s, 2.4 Mbit/s. */
export function formatBps(bps: number): string {
	if (!isFinite(bps) || bps <= 0) return '0';
	if (bps < 1000) return `${Math.round(bps)} bit/s`;
	if (bps < 1_000_000) return `${(bps / 1000).toFixed(1)} kbit/s`;
	return `${(bps / 1_000_000).toFixed(1)} Mbit/s`;
}

/** The cameras that look at one piece of equipment. */
export function camerasFor(list: Camera[], assetId: string): Camera[] {
	return list.filter((c) => c.asset === assetId);
}

/** "Live: 2 of 4 watching" (the cap, when the controller names one). */
export function watchingText(c: Pick<Camera, 'watching' | 'maxViewers'>): string {
	return c.maxViewers > 0 ? `Live: ${c.watching} of ${c.maxViewers} watching` : `Live: ${c.watching} watching`;
}

export const LIVE_LIMIT = 'The live view reached its time limit: click Watch live to continue.';

export const LIVE_CAPTION = 'Live video uses bandwidth, and over a slow link (radio, cellular) it can slow the tag values. Watching live is your choice.';

// ---- the panel's mode, pure ----

export type CameraMode = 'snapshot' | 'live' | 'busy' | 'error';

export interface CameraState {
	mode: CameraMode;
	/** The controller's words for the last failure; empty when none. */
	message: string;
}

export type CameraEvent =
	/** The person clicked Watch live. */
	| { type: 'watch' }
	/** The person clicked Stop. */
	| { type: 'stop' }
	/** The tab was hidden, or the panel is going away. */
	| { type: 'hidden' }
	| { type: 'destroyed' }
	/** The stream would not open or broke (status 429 is the viewer cap). */
	| { type: 'streamError'; status: number; message: string }
	/** A still failed. */
	| { type: 'snapshotError'; message: string }
	/** A still arrived. */
	| { type: 'snapshotOk' };

export const INITIAL: CameraState = { mode: 'snapshot', message: '' };

/**
 * The panel's mode. Live is entered only by `watch` (and only where the camera
 * has a live stream: `canLive`), and left by everything else, so a stream is
 * never left running by accident. A stream failure returns to snapshots and
 * keeps the controller's message (`busy` when it was the viewer cap).
 */
export function nextState(s: CameraState, e: CameraEvent, canLive = true): CameraState {
	switch (e.type) {
		case 'watch':
			return canLive ? { mode: 'live', message: '' } : s;
		case 'stop':
		case 'hidden':
		case 'destroyed':
			return s.mode === 'live' ? { mode: 'snapshot', message: '' } : s;
		case 'streamError':
			// a late error from a stream that was already stopped changes nothing
			if (s.mode !== 'live') return s;
			return { mode: e.status === 429 ? 'busy' : 'error', message: e.message };
		case 'snapshotError':
			return s.mode === 'live' ? s : { mode: s.mode === 'busy' ? 'busy' : 'error', message: e.message };
		case 'snapshotOk':
			return s.mode === 'error' ? { mode: 'snapshot', message: '' } : s;
	}
}

/** Live video is on. */
export const isLive = (s: CameraState): boolean => s.mode === 'live';
