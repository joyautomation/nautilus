// contract: docs/design/alarms.md
//
// TS types + a runes-based client for the nautilus `alarm/` package's HTTP
// API and its `frame.alarms` summary. House style, same shape as
// `realtime.svelte.ts`: state is exposed as Svelte 5 runes, fetching lives
// here (not in the components — `AlarmBanner`/`AlarmTable`/`AlarmJournal`
// stay props-in/callbacks-out), and the client degrades gracefully when the
// controller was built with no alarm definitions at all (`GET /api/alarms`
// 404s → `supported` flips to `false` rather than throwing).
import { kitFetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';
import { listOutOfService, returnToService, takeOutOfService, type OutOfService } from './outofservice.js';
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
 * Never color alone: every priority carries a label and a shape
 * (`PRIORITY_SIGN`, drawn by `<AlarmSign>`), the same convention as
 * `ConnectionBadge`'s `STATE_META`. Colors are the kit's reserved roles — no
 * new tokens (`--crit`/`--serious`/`--warn`/`--ink-2`/`--muted` cover
 * critical → diagnostic per the brief, §5). `glyph` is the shape as a
 * character (critical's hexagon is the nearest one an octagon has in most
 * fonts), for plain text only (a CSV, a notification); a screen draws
 * `<AlarmSign>`. */
export const PRIORITY_META: Record<Priority, { label: string; color: string; glyph: string }> = {
	critical: { label: 'Critical', color: 'var(--crit)', glyph: '⬣' },
	high: { label: 'High', color: 'var(--serious)', glyph: '▲' },
	medium: { label: 'Medium', color: 'var(--warn)', glyph: '◆' },
	low: { label: 'Low', color: 'var(--ink-2)', glyph: '●' },
	diagnostic: { label: 'Diagnostic', color: 'var(--muted)', glyph: '■' }
};

/**
 * How each priority is drawn, so its urgency reads before its colour does:
 * a distinct shape per level (colour-blind safe, and legible at a glance
 * across a rack), a mark inside it, and what the level asks of the operator
 * — how soon it must be acted on (ISA-18.2: priority is the time to
 * respond). Neither ISA-18.2 nor ISA-101 prescribes the shapes, only that
 * priority is never carried by colour alone.
 */
export interface PrioritySign {
	shape: 'octagon' | 'triangle' | 'diamond' | 'circle' | 'square';
	/** The mark inside the shape. */
	glyph: string;
	/** What the level asks for, in the operator's words. */
	urgency: string;
}

export const PRIORITY_SIGN: Record<string, PrioritySign> = {
	critical: { shape: 'octagon', glyph: '!!', urgency: 'act now' },
	high: { shape: 'triangle', glyph: '!', urgency: 'act within minutes' },
	medium: { shape: 'diamond', glyph: '!', urgency: 'act this shift' },
	low: { shape: 'circle', glyph: 'i', urgency: 'when convenient' },
	diagnostic: { shape: 'square', glyph: '?', urgency: 'for the record' }
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
	/** The piece of equipment it belongs to: out of service takes all of its alarms at once (#715). */
	equipment?: string;
	/** While suppressed: `out-of-service`, `designed`, `substituted` or `unreadable`. */
	suppression?: string;
	/** Why it is suppressed, in the controller's words (an out-of-service reason too). */
	reason?: string;
	/** Who took it out of service, and the epoch ms that ends (out of service only). */
	suppressedBy?: string;
	suppressUntilMs?: number;
	/** Set when its suppress-when could not be read, so it is shown. */
	warning?: string;
	/** Present only while the alarm is chattering (#1249). */
	chatter?: AlarmChatter;
	/** On an alarm whose last activation was seen as something came back
	 * ("site RTU9 came back"): it began sometime while it could not be seen,
	 * so `activeMs` is when it was seen, not when the fault began (#1249). */
	seenWhen?: string;
}

/** An alarm's chattering episode (`alarm.Chatter` in Go). */
export interface AlarmChatter {
	/** Epoch ms the episode began, and of its last activation. */
	sinceMs: number;
	lastMs: number;
	/** Activations in this episode; `episodes` counts every episode since the engine started. */
	activations: number;
	episodes: number;
	/** The detection rule in force: `count` activations within `windowMs`. */
	windowMs: number;
	count: number;
}

/** An alarm flood in progress, counts only (`alarm.FloodBrief` in Go). */
export interface AlarmFloodBrief {
	id: string;
	sinceMs: number;
	/** Activations in the current window; `total` since the flood began; `peak` the most in any window. */
	count: number;
	total: number;
	peak: number;
	windowMs: number;
	threshold: number;
	/** The ring overflowed: `count` and `peak` read as at least that many. */
	saturated?: boolean;
}

/** One piece of equipment's share of a flood. */
export interface AlarmFloodGroup {
	equipment: string;
	area?: string;
	count: number;
}

/** The flood with its first-out alarms and groups (`GET /api/alarms` `flood`). */
export interface AlarmFlood extends AlarmFloodBrief {
	/** The first activations of the flood, in time order. */
	firstOut: AlarmBrief[];
	/** The top equipment, most first. */
	groups: AlarmFloodGroup[];
}

/** One chattering alarm and what would have helped it (`GET /api/alarms/chatter`). */
export interface AlarmAdvice {
	id: string;
	name: string;
	priority: Priority;
	chatter: AlarmChatter;
	/** The delays the alarm runs with now, in ms. */
	onDelayMs: number;
	offDelayMs: number;
	/** Absent when no longer delay on the ladder would have stopped it. */
	suggestion?: {
		onDelayMs: number;
		offDelayMs: number;
		/** The most activations in one window, before and with the suggestion. */
		before: number;
		after: number;
		bounces: number;
		sinceMs: number;
		why: string;
	};
	/** The suggestion as a tuning overlay row, ready to paste. */
	row?: string;
	/** The journal read for it was cut (or it is past the cap): no suggestion was made. */
	cut?: boolean;
}

export interface AlarmAdviceAnswer {
	alarms: AlarmAdvice[];
	/** The limits of the replay, in words. */
	note: string;
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
	/** Alarms chattering now; omitted when none (#1249). */
	chattering?: number;
	/** The flood in progress, numbers only; omitted when there is none. */
	flood?: AlarmFloodBrief;
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
	| 'unsuppress'
	/** One notification an escalation policy sent or tried (#715); not a state change. */
	| 'notify'
	/** Detection rows (#1249); left out of a journal query unless named. */
	| 'chatter'
	| 'chatter-end'
	| 'flood'
	| 'flood-end';

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
	/** Which activation this row belongs to (#724): the key for the pictures kept when it activated. */
	occurrence?: string;
	/** Notify rows only (#715): the escalation level (1 first), who was
	 * paged (a principal, never an address), the channel kind, the result
	 * (`sent`, `failed`, `nobody-on-call`, and for #1249 `held` (a chattering
	 * alarm's pages held, one notice per level) and `grouped` (listed on a
	 * flood's grouped page)) and why when there is more to say. */
	level?: number;
	to?: string;
	via?: string;
	result?: string;
	reason?: string;
	/** Detection rows: activations counted, the most in any window, the
	 * window in ms, and when the episode began (an end row's `ts` minus it
	 * is its length). `standing` marks an `active` row that was not a new
	 * activation; `seenWhen` says what the alarm was seen coming back from. */
	count?: number;
	peak?: number;
	windowMs?: number;
	sinceMs?: number;
	standing?: boolean;
	seenWhen?: string;
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
	/** Every piece of equipment, quiet ones too (with ?suppressed=1). */
	equipment?: string[];
	summary: AlarmSummary;
	/** Present only during a flood. */
	flood?: AlarmFlood;
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
	/** Equipment out of service, the soonest to end first (`GET /api/alarms/out-of-service`). */
	outOfService = $state<OutOfService[]>([]);
	/** Every piece of equipment the alarms belong to, quiet ones too: what can be taken out of service. */
	equipment = $state<string[]>([]);
	/** The flood in progress, with first-out and groups; null when there is none. */
	flood = $state<AlarmFlood | null>(null);
	/** Delay suggestions for the alarms chattering now; null until asked for. */
	advice = $state<AlarmAdviceAnswer | null>(null);
	/** True while a fetch of the full list is in flight. */
	loading = $state(false);
	/** Last fetch/ack/shelve error message, if any. Cleared on the next
	 * successful call. */
	error = $state<string | null>(null);

	#url: string;
	#token?: string;
	#lastFetchedRev: number | null = null;
	#adviceSeq = 0;
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
		return this.instances.filter((a) => a.state !== 'shelved' && a.state !== 'suppressed');
	}

	/** Alarms chattering now, most activations first. */
	get chattering(): AlarmInstance[] {
		return this.instances.filter((a) => a.chatter).sort((a, b) => (b.chatter?.activations ?? 0) - (a.chatter?.activations ?? 0));
	}

	/** Alarms hidden by suppression (out of service, designed, substituted), with why. */
	get suppressed(): AlarmInstance[] {
		return this.instances.filter((a) => a.state === 'suppressed');
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
			const res = await kitFetch(`${this.#url}?suppressed=1`, { headers: authHeaders(this.#token, false) });
			if (res.status === 404) {
				this.supported = false;
				this.error = null;
				return;
			}
			if (!res.ok) {
				this.error = refusalMessage(res.status, await res.text(), `fetch failed`);
				return;
			}
			const body = (await res.json()) as AlarmsGetResponse;
			this.instances = body.alarms ?? [];
			this.equipment = body.equipment ?? [];
			this.flood = body.flood ?? null;
			this.summary = body.summary ?? this.summary;
			this.supported = true;
			this.error = null;
			this.#lastFetchedRev = body.summary?.rev ?? this.#lastFetchedRev;
			await this.refreshOutOfService();
		} catch {
			this.error = 'fetch failed — controller unreachable';
		} finally {
			this.loading = false;
		}
	}

	/** `GET /api/alarms/chatter`: what would have stopped each chattering alarm.
	 * Advisory and read-only; a failed read keeps the last answer. */
	async loadAdvice(): Promise<void> {
		const seq = ++this.#adviceSeq;
		try {
			const res = await kitFetch(`${this.#url}/chatter`, { headers: authHeaders(this.#token, false) });
			if (!res.ok) {
				if (seq === this.#adviceSeq) this.error = refusalMessage(res.status, await res.text(), `suggestions failed`);
				return;
			}
			const body = (await res.json()) as AlarmAdviceAnswer;
			// only the newest ask counts: a slow answer must not overwrite a newer one
			if (seq === this.#adviceSeq) this.advice = body;
		} catch {
			this.error = 'suggestions failed — controller unreachable';
		}
	}

	/** Forget the suggestions (nothing chatters now); a read still in flight is dropped. */
	clearAdvice(): void {
		this.#adviceSeq++;
		this.advice = null;
	}

	/** Read the out-of-service windows. A failed read keeps the last list. */
	async refreshOutOfService(): Promise<void> {
		const list = await listOutOfService(this.#url, authHeaders(this.#token, false));
		if (list) this.outOfService = list;
	}

	/** `POST /api/alarms/out-of-service`: every alarm of `equipment` hidden for `seconds` (a week at most),
	 * with a reason. Needs a fresh password; the kit's fetch asks for it. True when the controller took it. */
	async takeOutOfService(equipment: string, seconds: number, reason: string, by: string): Promise<boolean> {
		const r = await takeOutOfService(this.#url, { equipment, seconds, reason, by }, authHeaders(this.#token, true));
		this.error = r.ok ? null : r.message;
		if (r.ok) await this.refresh();
		return r.ok;
	}

	/** `POST /api/alarms/in-service`. */
	async returnToService(equipment: string, by: string): Promise<boolean> {
		const r = await returnToService(this.#url, { equipment, by }, authHeaders(this.#token, true));
		this.error = r.ok ? null : r.message;
		if (r.ok) await this.refresh();
		return r.ok;
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
			const res = await kitFetch(`${this.#url}/ack`, {
				method: 'POST',
				headers: authHeaders(this.#token, true),
				body: JSON.stringify(body)
			});
			if (!res.ok) {
				this.error = refusalMessage(res.status, await res.text(), `ack failed`);
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
	async shelve(
		id: string,
		seconds: number,
		by: string,
		why?: { reason: string; via?: 'chatter' }
	): Promise<AlarmInstance | null> {
		const until = new Date(Date.now() + seconds * 1000).toISOString();
		return this.#writeOne('shelve', { id, until, by, ...(why ? { reason: why.reason, via: why.via } : {}) });
	}

	/** `POST /api/alarms/unshelve`. */
	async unshelve(id: string, by: string): Promise<AlarmInstance | null> {
		return this.#writeOne('unshelve', { id, by });
	}

	async #writeOne(action: 'shelve' | 'unshelve', body: Record<string, unknown>): Promise<AlarmInstance | null> {
		try {
			const res = await kitFetch(`${this.#url}/${action}`, {
				method: 'POST',
				headers: authHeaders(this.#token, true),
				body: JSON.stringify(body)
			});
			if (!res.ok) {
				this.error = refusalMessage(res.status, await res.text(), `${action} failed`);
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
			const res = await kitFetch(`${this.#url}/journal?${q}`, { headers: authHeaders(this.#token, false) });
			if (!res.ok) {
				this.error = refusalMessage(res.status, await res.text(), `journal fetch failed`);
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
