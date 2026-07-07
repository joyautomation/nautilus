// App-agnostic realtime client for nautilus HMIs.
//
// One SSE stream in, commands out over POST. State is exposed as Svelte 5 runes
// so components react directly. Unlike a runtime-specific client, this knows
// nothing about the frame shape: it hands you the latest parsed JSON frame
// (typed by a generic parameter) and lets you subscribe to frames to fan them
// out into your own trend buffers.
//
// Connection health is judged by *data freshness*, not EventSource events
// (which lie across a proxy failover): `connected` is true only while a frame
// arrived within `freshnessMs`. The stream self-heals — on error it tears the
// EventSource down and reconnects on a fixed interval.
import type { TrendPoint } from './types.js';

export type { TrendPoint };

/**
 * A rolling window of timestamped samples, trimmed to `windowS` seconds.
 * Reactive: read `.points` in a component and it re-renders as samples arrive.
 */
export class TrendBuffer {
	points = $state<TrendPoint[]>([]);
	#windowMs: number;

	constructor(windowS = 300) {
		this.#windowMs = windowS * 1000;
	}

	/** Append one sample and drop anything older than the window. */
	push(t: number, v: number) {
		const cutoff = t - this.#windowMs;
		const next = this.points.filter((p) => p.t >= cutoff);
		next.push({ t, v });
		this.points = next;
	}

	// Merge historian points into the buffer, deduped by timestamp (existing
	// values win) and trimmed to the window. Idempotent, so it can run on every
	// (re)connect to fill any gap.
	seed(pts: TrendPoint[]) {
		const cutoff = Date.now() - this.#windowMs;
		const byT = new Map<number, number>();
		for (const p of pts) if (p.t >= cutoff) byT.set(p.t, p.v);
		for (const p of this.points) if (p.t >= cutoff) byT.set(p.t, p.v);
		this.points = [...byT.entries()].map(([t, v]) => ({ t, v })).sort((a, b) => a.t - b.t);
	}

	clear() {
		this.points = [];
	}
}

export interface RealtimeOptions<T> {
	/** SSE endpoint. Default `/api/stream`. */
	url?: string;
	/** Consider the link healthy while a frame arrived within this many ms. Default 3000. */
	freshnessMs?: number;
	/** Delay before reconnecting after a stream error, in ms. Default 1000. */
	reconnectMs?: number;
	/** Parse a raw SSE `data` string into a frame. Default `JSON.parse`. */
	parse?: (data: string) => T;
	/** Called for every frame. Use it to push values into TrendBuffers. */
	onFrame?: (frame: T) => void;
}

/**
 * A realtime SSE client exposing the latest frame and connection freshness as
 * runes. Generic over the frame shape `T`.
 *
 * ```ts
 * const rt = new RealtimeClient<MySnapshot>({ onFrame: (s) => level.push(s.ts, s.level) });
 * rt.start();
 * // in a component: rt.frame?.level, rt.connected
 * ```
 */
export class RealtimeClient<T = unknown> {
	/** True while a frame arrived within `freshnessMs`. */
	connected = $state(false);
	/** The most recent parsed frame, or null before the first message. */
	frame = $state<T | null>(null);
	/** Epoch ms of the last frame received. */
	lastMessageAt = $state(0);

	#url: string;
	#freshnessMs: number;
	#reconnectMs: number;
	#parse: (data: string) => T;
	#subs = new Set<(frame: T) => void>();

	#es: EventSource | null = null;
	#reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	#heartbeat: ReturnType<typeof setInterval> | null = null;
	#onOpen: (() => void) | null = null;

	constructor(opts: RealtimeOptions<T> = {}) {
		this.#url = opts.url ?? '/api/stream';
		this.#freshnessMs = opts.freshnessMs ?? 3000;
		this.#reconnectMs = opts.reconnectMs ?? 1000;
		this.#parse = opts.parse ?? ((d) => JSON.parse(d) as T);
		if (opts.onFrame) this.#subs.add(opts.onFrame);
	}

	/** Subscribe to frames. Returns an unsubscribe function. */
	onFrame(cb: (frame: T) => void): () => void {
		this.#subs.add(cb);
		return () => this.#subs.delete(cb);
	}

	/** Register a callback run whenever the stream (re)opens — e.g. to backfill. */
	onOpen(cb: () => void) {
		this.#onOpen = cb;
	}

	/** Open the stream and begin tracking freshness. Idempotent. */
	start() {
		if (this.#es || this.#reconnectTimer) return;
		this.#connect();
		// Freshness heartbeat: green if a frame arrived within the window, red
		// otherwise. Self-heals when data resumes.
		this.#heartbeat ??= setInterval(() => {
			this.connected = Date.now() - this.lastMessageAt < this.#freshnessMs;
		}, 500);
	}

	/** Close the stream and stop all timers. */
	stop() {
		this.#es?.close();
		this.#es = null;
		if (this.#reconnectTimer) clearTimeout(this.#reconnectTimer);
		this.#reconnectTimer = null;
		if (this.#heartbeat) clearInterval(this.#heartbeat);
		this.#heartbeat = null;
		this.connected = false;
	}

	#connect() {
		const es = new EventSource(this.#url);
		this.#es = es;
		es.onopen = () => this.#onOpen?.();
		es.onerror = () => {
			// The built-in reconnect can stall across a proxy failover — tear the
			// stream down and reconnect ourselves on a fixed interval.
			es.close();
			this.#es = null;
			this.#reconnectTimer ??= setTimeout(() => {
				this.#reconnectTimer = null;
				this.#connect();
			}, this.#reconnectMs);
		};
		es.onmessage = (ev) => {
			this.lastMessageAt = Date.now();
			this.connected = true;
			let f: T;
			try {
				f = this.#parse(ev.data);
			} catch {
				return; // ignore malformed frames
			}
			this.frame = f;
			for (const cb of this.#subs) cb(f);
		};
	}

	/**
	 * Fire-and-forget POST command to a JSON endpoint. Default `/api/command`,
	 * body `{ cmd, ...fields }`.
	 */
	async send(cmd: string, fields: Record<string, unknown> = {}, url = '/api/command') {
		await fetch(url, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ cmd, ...fields })
		});
	}
}

/** Convenience factory mirroring `new RealtimeClient<T>(opts)`. */
export function createRealtimeClient<T = unknown>(opts: RealtimeOptions<T> = {}): RealtimeClient<T> {
	return new RealtimeClient<T>(opts);
}
