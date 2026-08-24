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
import { emptyDelta, mergeDelta, type DeltaState } from './delta.js';
import type { Quality, TrendPoint } from './types.js';

export type { Quality, TrendPoint };

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

/**
 * The half of `RealtimeClient` that everything downstream of a frame
 * actually uses — `useTrend`, `AlarmClient`, any consumer that wires itself
 * to frames rather than being handed one.
 *
 * It is an interface rather than the class so an app can put its OWN object
 * in front of the client: the Pomona HMI swaps the underlying connection
 * whenever the open screen changes what it needs (`?tags=`, fixed at
 * connect), and hands the kit a stable facade that forwards to whichever
 * connection is live. Trend buffers are keyed on this identity, so a facade
 * is what keeps a sparkline's history across that swap.
 */
export interface FrameSource<T> {
	/** The most recent frame, or null before the first one. */
	readonly frame: T | null;
	/** Subscribe to frames. Returns an unsubscribe function. */
	onFrame(cb: (frame: T) => void): () => void;
	/** Subscribe to (re)opens — the backfill hook. Returns an unsubscribe. */
	onOpen(cb: () => void): () => void;
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
	/**
	 * Subscribe to only these tags — glob patterns matched against the
	 * controller's dotted tag names (`['RTU9_*', '*_LIT_*']`). The
	 * controller applies them to every frame including the first, so a
	 * screen that draws forty points pulls forty points instead of the
	 * whole plant.
	 *
	 * Patterns match WHOLE tag names, and a dot is an ordinary character:
	 * `Tank*` matches both `Tank101` and `Tank101.Level`. Omit for
	 * everything (the default, and what every existing caller gets).
	 */
	tags?: string[];
	/**
	 * Ask the controller for DELTA frames: after the first full snapshot,
	 * each frame carries only the tags that changed, and the client merges
	 * them into a complete `frame.tags` for you. Default **true** — the
	 * whole point is that consumers see no difference.
	 *
	 * It is measured, not theoretical: on a 10,000-tag controller at 5%
	 * churn a full frame is ~280 kB and a delta ~17 kB — about 17× less on
	 * the wire, per client, per tick. That is the difference between one
	 * wall screen and a shift's worth of tablets.
	 *
	 * Falls back to plain pass-through automatically against a controller
	 * that does not implement deltas (its frames carry no `seq`), so
	 * turning it on is never a compatibility risk. Set `false` to force
	 * whole frames — e.g. when the frame shape is not nautilus's.
	 */
	delta?: boolean;
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

	/**
	 * Frames received on this connection, from 1 — the delta stream's own
	 * counter, reset on every (re)connect. Zero on a plain stream.
	 */
	seq = $state(0);
	/**
	 * How many times a detected gap in `seq` forced a reconnect. Normally
	 * zero for the life of a session; a climbing number is a transport
	 * problem (a proxy rewriting bodies, a flaky link), not a tuning knob.
	 */
	resyncs = $state(0);

	/**
	 * Frames received on this client since it was created, across every
	 * reconnect — unlike `seq`, which is the CONNECTION's counter and
	 * restarts at 1 each time the stream reopens.
	 */
	frames = $state(0);
	/**
	 * Payload characters received since this client was created: the sum of
	 * every `data:` line's length, excluding SSE framing and HTTP headers.
	 *
	 * It exists to be measured. "Filtering the subscription made the screen
	 * cheaper" is a claim about bytes, and the alternative to counting them
	 * here is reading a browser network panel by hand — which cannot
	 * separate two streams on one page, and cannot be asserted on at all.
	 * Tag names and values are ASCII in every nautilus frame, so characters
	 * and bytes are the same number in practice; treat it as approximate if
	 * yours are not.
	 */
	bytesReceived = $state(0);

	#url: string;
	#freshnessMs: number;
	#reconnectMs: number;
	#parse: (data: string) => T;
	#subs = new Set<(frame: T) => void>();
	#tags: string[];
	#delta: boolean;
	/**
	 * The accumulated state of the delta subscription. The merge rules
	 * themselves live in ./delta.ts as a pure function — the one piece of
	 * this client that can silently show an operator a value the plant does
	 * not have, so it is kept readable and testable without a browser.
	 */
	#deltaState: DeltaState = emptyDelta();

	#es: EventSource | null = null;
	#reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	#heartbeat: ReturnType<typeof setInterval> | null = null;
	#opens = new Set<() => void>();

	constructor(opts: RealtimeOptions<T> = {}) {
		this.#url = opts.url ?? '/api/stream';
		this.#freshnessMs = opts.freshnessMs ?? 3000;
		this.#reconnectMs = opts.reconnectMs ?? 1000;
		this.#parse = opts.parse ?? ((d) => JSON.parse(d) as T);
		this.#tags = opts.tags ?? [];
		this.#delta = opts.delta ?? true;
		if (opts.onFrame) this.#subs.add(opts.onFrame);
	}

	/**
	 * The glob patterns this client subscribed to, or `[]` for the
	 * unfiltered stream. Fixed at construction — the controller applies
	 * `?tags=` per connection, so CHANGING a subscription means opening a
	 * new client, not mutating this one.
	 */
	get tagFilter(): string[] {
		return [...this.#tags];
	}

	/**
	 * The stream URL actually opened, with the subscription parameters this
	 * client was configured with. Exposed because "why is my screen empty"
	 * is almost always answered by reading it.
	 */
	get streamUrl(): string {
		const qs = new URLSearchParams();
		if (this.#tags.length) qs.set('tags', this.#tags.join(','));
		if (this.#delta) qs.set('delta', '1');
		const q = qs.toString();
		if (!q) return this.#url;
		return this.#url + (this.#url.includes('?') ? '&' : '?') + q;
	}

	/**
	 * A tag's data quality as the controller reports it — `'good'` when it
	 * reports nothing, which is also what an older controller (and any
	 * non-nautilus frame) always yields.
	 *
	 * A DOTTED path resolves to its root tag when the root is the one
	 * carrying quality: a UDT arrives from its source whole, so
	 * `P101.Drive.Speed` is exactly as trustworthy as `P101`. An exact
	 * entry for the full path still wins, for a controller that reports at
	 * member granularity.
	 *
	 * Check `/api/meta`'s `quality` flag before rendering a quality badge:
	 * `'good'` here means "nothing said it was bad", which on a controller
	 * that cannot report quality is not the same as "verified good".
	 */
	quality(tag: string): Quality {
		const q = (this.frame as { quality?: Record<string, Quality> } | null)?.quality;
		if (!q) return 'good';
		const exact = q[tag];
		if (exact) return exact;
		const dot = tag.indexOf('.');
		return (dot > 0 ? q[tag.slice(0, dot)] : undefined) ?? 'good';
	}

	/**
	 * Whether a tag's value can be shown without qualification — the
	 * predicate a bound control wants, so nobody re-derives
	 * `quality(t) === 'good'` and forgets a case when a fifth value
	 * appears.
	 */
	isGood(tag: string): boolean {
		return this.quality(tag) === 'good';
	}

	/** Subscribe to frames. Returns an unsubscribe function. */
	onFrame(cb: (frame: T) => void): () => void {
		this.#subs.add(cb);
		return () => this.#subs.delete(cb);
	}

	/**
	 * Register a callback run whenever the stream (re)opens — e.g. to backfill
	 * a trend from the historian and close whatever gap the outage left.
	 * Returns an unsubscribe function; several subscribers may coexist.
	 */
	onOpen(cb: () => void): () => void {
		this.#opens.add(cb);
		return () => this.#opens.delete(cb);
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
		this.#resetDelta();
	}

	// A new connection is a new subscription: the controller sends a fresh
	// full frame, so any merged state from the old one must go. Keeping it
	// is the one way a delta client can end up showing a value the
	// controller no longer holds.
	#resetDelta() {
		this.#deltaState = emptyDelta();
		this.seq = 0;
	}

	/**
	 * Merge one incoming frame and return the frame a consumer should see —
	 * always a COMPLETE one, so nothing downstream has to know whether
	 * deltas are on. Null means a gap was detected and the stream is being
	 * reopened; publish nothing for this frame.
	 *
	 * See ./delta.ts for the protocol and the rules.
	 */
	#mergeFrame(f: T): T | null {
		const r = mergeDelta(this.#deltaState, f);
		if (r.gap) {
			this.resyncs++;
			this.#reconnect();
			return null;
		}
		this.seq = this.#deltaState.seq;
		return r.frame;
	}

	// Tear the stream down and open a new one now — the resync path. The
	// EventSource's own retry is deliberately not used: it can stall across
	// a proxy failover, which is the same reason #connect handles errors
	// itself.
	#reconnect() {
		this.#es?.close();
		this.#es = null;
		this.#resetDelta();
		if (this.#reconnectTimer) return;
		this.#reconnectTimer = setTimeout(() => {
			this.#reconnectTimer = null;
			this.#connect();
		}, 0);
	}

	#connect() {
		const es = new EventSource(this.streamUrl);
		this.#es = es;
		es.onopen = () => {
			// A reopened stream restarts the controller's sequence at 1 and
			// begins with a full frame; anything merged before belongs to
			// the connection that just died.
			this.#resetDelta();
			for (const cb of this.#opens) cb();
		};
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
			this.frames++;
			this.bytesReceived += ev.data.length;
			let f: T;
			try {
				f = this.#parse(ev.data);
			} catch {
				return; // ignore malformed frames
			}
			const merged = this.#delta ? this.#mergeFrame(f) : f;
			if (merged === null) return; // gap detected; reconnecting
			this.frame = merged;
			for (const cb of this.#subs) cb(merged);
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

	/**
	 * Write one tag through `POST /api/tags`, and return the controller's
	 * reason when it refuses — `null` on success. A faceplate shows that reason
	 * on the control instead of pretending the command landed.
	 *
	 * `name` is a whole tag (`'TempSP'`) or a dotted MEMBER path of a UDT tag
	 * (`'WEL15_SUP_015.START'`, `'AI_001.LVL.CTL1HSP'`) — the same paths the
	 * frame's nested tag objects are read with, so a bound control writes back
	 * exactly what it displays. `value` may also be an object to set several
	 * members of one struct tag at once; members it omits keep their current
	 * value.
	 *
	 * The controller refuses a path that resolves to nothing (an unknown tag or
	 * a misspelled member) rather than creating a tag — so a typo surfaces here
	 * as a message, not as a phantom tag on the wire. A member of a
	 * driver-owned input is refused for the same reason it is not editable in
	 * the dashboard: the driver overwrites the whole tag before the next scan.
	 *
	 * `token` sets `X-Nautilus-Token` for a cross-origin writer; same-origin
	 * pages (the usual deployment, and a dev proxy that rewrites Origin) need
	 * nothing.
	 */
	async writeTag(
		name: string,
		value: unknown,
		opts: { url?: string; token?: string } = {}
	): Promise<string | null> {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (opts.token) headers['X-Nautilus-Token'] = opts.token;
		try {
			const res = await fetch(opts.url ?? '/api/tags', {
				method: 'POST',
				headers,
				body: JSON.stringify({ name, value })
			});
			if (res.ok) return null;
			const detail = (await res.text()).trim();
			if (res.status === 401 || res.status === 403) {
				return detail || 'write refused — this controller requires an auth token';
			}
			return detail || `write failed (${res.status})`;
		} catch {
			return 'write failed — controller unreachable';
		}
	}
}

/** Convenience factory mirroring `new RealtimeClient<T>(opts)`. */
export function createRealtimeClient<T = unknown>(opts: RealtimeOptions<T> = {}): RealtimeClient<T> {
	return new RealtimeClient<T>(opts);
}
