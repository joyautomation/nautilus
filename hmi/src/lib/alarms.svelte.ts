// contract: docs/design/alarms.md
//
// TS types + a runes-based client for the nautilus `alarm/` package's HTTP
// API and its `frame.alarms` summary. House style, same shape as
// `realtime.svelte.ts`: state is exposed as Svelte 5 runes, fetching lives
// here (not in the components — `AlarmBanner`/`AlarmTable`/`AlarmJournal`
// stay props-in/callbacks-out), and the client degrades gracefully when the
// controller was built with no alarm definitions at all (`GET /api/alarms`
// 404s → `supported` flips to `false` rather than throwing).
import type { FrameSource } from './realtime.svelte.js';

/** ISA-18.2 priority, JSON lowercase. Ordered low → high for sorting. */
export type Priority = 'diagnostic' | 'low' | 'medium' | 'high' | 'critical';

/** Sort weight, low → high — `PRIORITY_ORDER.indexOf(a) - PRIORITY_ORDER.indexOf(b)`. */
export const PRIORITY_ORDER: Priority[] = ['diagnostic', 'low', 'medium', 'high', 'critical'];

/**
 * ISA-18.2 alarm state, JSON lowercase-with-hyphens (mirrors Go's `State`
 * String() per the brief's transition table, §1).
 *
 * - `unack-active` — condition is true, operator has not acknowledged.
 * - `ack-active` — condition is true, acknowledged.
 * - `unack-rtn` — condition returned to normal but not yet acknowledged.
 * - `shelved` — time-boxed suppression; the prior state is restored on expiry.
 * - `suppressed` — `enable` tag false, or the source tag is missing.
 */
export type AlarmState =
	| 'normal'
	| 'unack-active'
	| 'ack-active'
	| 'unack-rtn'
	| 'shelved'
	| 'suppressed';

/** Module-scope metadata for each priority — reused by the banner and table.
 * Never color alone: every priority carries a label and a glyph, the same
 * convention as `ConnectionBadge`'s `STATE_META`. Colors are the kit's
 * reserved roles — no new tokens (`--crit`/`--serious`/`--warn`/`--ink-2`/
 * `--muted` cover critical → diagnostic per the brief, §5). */
export const PRIORITY_META: Record<Priority, { label: string; color: string; glyph: string }> = {
	critical: { label: 'Critical', color: 'var(--crit)', glyph: '■' },
	high: { label: 'High', color: 'var(--serious)', glyph: '▲' },
	medium: { label: 'Medium', color: 'var(--warn)', glyph: '▲' },
	low: { label: 'Low', color: 'var(--ink-2)', glyph: '●' },
	diagnostic: { label: 'Diagnostic', color: 'var(--muted)', glyph: '○' }
};

/** Module-scope metadata for each ISA-18.2 state — label, color, and whether
 * it should flash. Per the brief's convention (§5 table, §0 states table):
 * unack-active is red and flashing (new, unacknowledged, still active);
 * ack-active is red steady (acknowledged, still active); unack-rtn is
 * blue/green (back to normal, just needs acknowledgment — not urgent, so it
 * borrows the "good" role rather than a warning one); shelved and suppressed
 * are grey. */
export const STATE_META: Record<
	AlarmState,
	{ label: string; color: string; flash: boolean }
> = {
	'unack-active': { label: 'Unack', color: 'var(--crit)', flash: true },
	'ack-active': { label: 'Acked', color: 'var(--crit)', flash: false },
	'unack-rtn': { label: 'RTN — Unack', color: 'var(--accent)', flash: false },
	normal: { label: 'Normal', color: 'var(--good)', flash: false },
	shelved: { label: 'Shelved', color: 'var(--muted)', flash: false },
	suppressed: { label: 'Suppressed', color: 'var(--muted)', flash: false }
};

/** The Perspective table's shelving durations, verbatim (brief §5) — seconds. */
export const DEFAULT_SHELVE_TIMES_S: number[] = [
	5 * 60,
	15 * 60,
	30 * 60,
	60 * 60,
	2 * 60 * 60,
	4 * 60 * 60,
	8 * 60 * 60
];

/** One instance: a `Def` plus its live state (`alarm.Record` in Go — `GET
 * /api/alarms`'s `alarms[]`, brief §1/§3). `id` is the join key everywhere
 * (row key, ack/shelve target, journal `id`). */
export interface AlarmInstance {
	id: string;
	tag: string;
	name: string;
	priority: Priority;
	class?: string;
	site?: string;
	area?: string;
	display?: string;
	notes?: string;
	/** BOOL that must be true or the definition is Suppressed; "" = always enabled. */
	enable?: string;
	ackRequired?: boolean;
	autoClear?: boolean;
	shelvable?: boolean;

	state: AlarmState;
	/** The raw condition bit, post on/off-delay. */
	cond: boolean;
	/** Epoch ms this instance last went active (Unack-Active), if currently active/RTN/shelved. */
	activeMs?: number;
	/** Epoch ms the condition last returned to normal. */
	rtnMs?: number;
	/** Epoch ms it was last acknowledged. */
	ackMs?: number;
	/** Epoch ms a shelf expires, if shelved. */
	shelfUntilMs?: number;
	ackBy?: string;
	shelfBy?: string;
	/** Activations since restart — flood/chatter indicator. */
	count?: number;
}

/** The newest unacknowledged alarm, carried on the summary for the banner
 * (`alarm.Brief` in Go). */
export interface AlarmBrief {
	id: string;
	name: string;
	priority: Priority;
	ms: number;
}

/** Counts-only rollup (`alarm.Summary` in Go) — this is what rides the SSE
 * frame every scan (`frame.alarms`, `server.Frame.Alarms`, brief §3); never
 * the full alarm list. `rev` bumps on any change, which is exactly the
 * signal `createAlarmClient` watches to know when to refetch `/api/alarms`. */
export interface AlarmSummary {
	active: number;
	unacked: number;
	shelved: number;
	suppressed: number;
	byPriority: Partial<Record<Priority, number>>;
	worst?: Priority;
	newest?: AlarmBrief;
	rev: number;
}

/** One append-only journal row (`alarm.Event` in Go — `GET
 * /api/alarms/journal`'s `events[]`). */
export type AlarmEventKind =
	| 'active'
	| 'rtn'
	| 'ack'
	| 'shelve'
	| 'unshelve'
	| 'suppress'
	| 'unsuppress';

export interface AlarmEvent {
	/** Epoch ms, from the runtime Clock — so acceptance tests replay deterministically. */
	ts: number;
	id: string;
	name: string;
	kind: AlarmEventKind;
	priority?: Priority;
	site?: string;
	state?: AlarmState;
	by?: string;
}

/** Query filters for `journal()` — mirrors `alarm.Filter` / the
 * `/api/alarms/journal` query string (brief §3). */
export interface AlarmJournalFilter {
	sites?: string[];
	priorities?: Priority[];
	ids?: string[];
	kinds?: AlarmEventKind[];
	limit?: number;
}

export interface AlarmJournalResult {
	events: AlarmEvent[];
	truncated: boolean;
}

interface AlarmsGetResponse {
	ts: number;
	summary: AlarmSummary;
	alarms: AlarmInstance[];
}

/** A frame carrying the optional alarm summary — matches the additive
 * `alarms?` field on `NautilusFrame` (types.ts). Generic so any host frame
 * shape works so long as it has this one field. */
export interface FrameWithAlarms {
	alarms?: AlarmSummary;
}

export interface AlarmClientOptions {
	/** Base for the alarm routes. Default `/api/alarms` — journal/ack/shelve/
	 * unshelve are relative to it (`${url}/journal`, `${url}/ack`, …). */
	url?: string;
	/** Cross-origin writer, forwarded as `X-Nautilus-Token` — same convention
	 * as `RealtimeClient.writeTag`. */
	token?: string;
}

function authHeaders(token: string | undefined, json: boolean): Record<string, string> {
	const h: Record<string, string> = {};
	if (json) h['Content-Type'] = 'application/json';
	if (token) h['X-Nautilus-Token'] = token;
	return h;
}

/**
 * Pure decision function behind the client's refetch trigger, factored out
 * so it is unit-testable without a network or a component tree: refetch iff
 * this is the first frame carrying `alarms` at all, or its `rev` differs
 * from the last one we fetched against. A frame with no `alarms` field
 * (an older controller, or one build with no alarm engine) never triggers
 * a fetch — `supported` only flips to `false` once `/api/alarms` itself
 * 404s.
 */
export function shouldRefetch(lastFetchedRev: number | null, frameRev: number | undefined): boolean {
	if (frameRev === undefined) return false;
	return lastFetchedRev === null || frameRev !== lastFetchedRev;
}

/**
 * Alarm client: wires a `RealtimeClient`'s frames to `/api/alarms`,
 * refetching the full active/shelved list only when `frame.alarms.rev`
 * changes (never on every frame — the summary alone is cheap enough to ride
 * every frame, but the list is not). Exposes runes so components react
 * directly; components themselves stay pure props-in/callbacks-out and
 * never call fetch.
 *
 * ```ts
 * const alarms = createAlarmClient(rt, { url: '/api/alarms' });
 * onMount(() => alarms.start());
 * // in a component: alarms.active, alarms.summary, alarms.supported
 * ```
 */
export class AlarmClient {
	/** Full active + unack-RTN + shelved list from the last successful fetch. */
	instances = $state<AlarmInstance[]>([]);
	/** Latest summary — updated from every frame that carries one, not just on refetch. */
	summary = $state<AlarmSummary | null>(null);
	/** `false` once `GET /api/alarms` has 404'd: this controller has no alarm
	 * engine. Starts `true` (optimistic) until the first response is in, so a
	 * banner can simply render nothing while `supported` is still unknown. */
	supported = $state(true);
	/** True while a fetch of the full list is in flight. */
	loading = $state(false);
	/** Last fetch/ack/shelve error message, if any. Cleared on the next
	 * successful call. */
	error = $state<string | null>(null);

	#url: string;
	#token?: string;
	#lastFetchedRev: number | null = null;
	#unsub: (() => void) | null = null;

	constructor(realtime: FrameSource<FrameWithAlarms> | null, opts: AlarmClientOptions = {}) {
		this.#url = opts.url ?? '/api/alarms';
		this.#token = opts.token;
		if (realtime) {
			this.#unsub = realtime.onFrame((f) => this.#onFrame(f));
			if (realtime.frame) this.#onFrame(realtime.frame);
		}
	}

	get active(): AlarmInstance[] {
		return this.instances.filter((a) => a.state !== 'shelved');
	}

	get shelved(): AlarmInstance[] {
		return this.instances.filter((a) => a.state === 'shelved');
	}

	#onFrame(frame: FrameWithAlarms) {
		if (frame.alarms) this.summary = frame.alarms;
		if (shouldRefetch(this.#lastFetchedRev, frame.alarms?.rev)) void this.refresh();
	}

	/** Fetch `GET /api/alarms` unconditionally — call once on mount (before
	 * the first frame arrives) and let frame-driven `rev` changes handle the
	 * rest. Safe to call repeatedly; concurrent calls are not de-duplicated
	 * beyond the `loading` flag being informational only. */
	async refresh(): Promise<void> {
		this.loading = true;
		try {
			const res = await fetch(this.#url, { headers: authHeaders(this.#token, false) });
			if (res.status === 404) {
				this.supported = false;
				this.error = null;
				return;
			}
			if (!res.ok) {
				this.error = (await res.text()).trim() || `fetch failed (${res.status})`;
				return;
			}
			const body = (await res.json()) as AlarmsGetResponse;
			this.instances = body.alarms ?? [];
			this.summary = body.summary ?? this.summary;
			this.supported = true;
			this.error = null;
			this.#lastFetchedRev = body.summary?.rev ?? this.#lastFetchedRev;
		} catch {
			this.error = 'fetch failed — controller unreachable';
		} finally {
			this.loading = false;
		}
	}

	/** `start()` mirrors `RealtimeClient` — call once on mount to seed the
	 * list; frame-driven refetches take over from there. */
	async start(): Promise<void> {
		await this.refresh();
	}

	/** Stop listening to the realtime client's frames. */
	stop() {
		this.#unsub?.();
		this.#unsub = null;
	}

	/** `POST /api/alarms/ack` — `ids: '*'` (or omit `ids`) acks everything. */
	async ack(ids: string[] | 'all', by: string): Promise<number> {
		const body = ids === 'all' ? { all: true, by } : { ids, by };
		try {
			const res = await fetch(`${this.#url}/ack`, {
				method: 'POST',
				headers: authHeaders(this.#token, true),
				body: JSON.stringify(body)
			});
			if (!res.ok) {
				this.error = (await res.text()).trim() || `ack failed (${res.status})`;
				return 0;
			}
			this.error = null;
			const out = (await res.json()) as { acked?: number };
			await this.refresh();
			return out.acked ?? 0;
		} catch {
			this.error = 'ack failed — controller unreachable';
			return 0;
		}
	}

	/** `POST /api/alarms/shelve` — `seconds` from now, converted to the ISO
	 * `until` the server expects. Pick one of `DEFAULT_SHELVE_TIMES_S` (or the
	 * project's own `shelveTimes` prop) for the duration picker. */
	async shelve(id: string, seconds: number, by: string): Promise<AlarmInstance | null> {
		const until = new Date(Date.now() + seconds * 1000).toISOString();
		return this.#writeOne('shelve', { id, until, by });
	}

	/** `POST /api/alarms/unshelve`. */
	async unshelve(id: string, by: string): Promise<AlarmInstance | null> {
		return this.#writeOne('unshelve', { id, by });
	}

	async #writeOne(action: 'shelve' | 'unshelve', body: Record<string, unknown>): Promise<AlarmInstance | null> {
		try {
			const res = await fetch(`${this.#url}/${action}`, {
				method: 'POST',
				headers: authHeaders(this.#token, true),
				body: JSON.stringify(body)
			});
			if (!res.ok) {
				this.error = (await res.text()).trim() || `${action} failed (${res.status})`;
				return null;
			}
			this.error = null;
			const rec = (await res.json()) as AlarmInstance;
			await this.refresh();
			return rec;
		} catch {
			this.error = `${action} failed — controller unreachable`;
			return null;
		}
	}

	/** `GET /api/alarms/journal?from&to&...` — `from`/`to` are epoch ms
	 * (matching every other timestamp in this kit). Does not touch `instances`
	 * or `summary`; callers hold onto the result themselves (e.g. `AlarmJournal`'s
	 * own state), the same way `TrendBuffer.seed` is caller-driven rather than
	 * auto-wired. */
	async journal(from: number, to: number, filters: AlarmJournalFilter = {}): Promise<AlarmJournalResult> {
		const q = new URLSearchParams({ from: String(from), to: String(to) });
		if (filters.sites?.length) q.set('site', filters.sites.join(','));
		if (filters.priorities?.length) q.set('priority', filters.priorities.join(','));
		if (filters.ids?.length) q.set('id', filters.ids.join(','));
		if (filters.kinds?.length) q.set('kind', filters.kinds.join(','));
		if (filters.limit) q.set('limit', String(filters.limit));
		try {
			const res = await fetch(`${this.#url}/journal?${q}`, { headers: authHeaders(this.#token, false) });
			if (!res.ok) {
				this.error = (await res.text()).trim() || `journal fetch failed (${res.status})`;
				return { events: [], truncated: false };
			}
			this.error = null;
			return (await res.json()) as AlarmJournalResult;
		} catch {
			this.error = 'journal fetch failed — controller unreachable';
			return { events: [], truncated: false };
		}
	}
}

/** Factory mirroring `createRealtimeClient` — pass the app's `RealtimeClient`
 * (typed with a frame that has an optional `alarms` field) to wire refetches
 * to `frame.alarms.rev`, or `null` to drive the client by hand (`refresh()`/
 * `start()`) with no realtime frame source. */
export function createAlarmClient(
	realtime: FrameSource<FrameWithAlarms> | null,
	opts: AlarmClientOptions = {}
): AlarmClient {
	return new AlarmClient(realtime, opts);
}
