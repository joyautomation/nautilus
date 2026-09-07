// The historian side of a trend: `GET /api/history*`, and splicing what it
// returns onto what the SSE stream has seen live.
//
// nautilus's historian is optional (`server.historian` /
// `NAUTILUS_HISTORIAN_URL`). Without one, every route here answers 503 — which
// is a legitimate, expected answer, not an error to throw on. Everything below
// therefore reports availability as data (`ok: false`) so the caller can say
// "live only, from when this page opened" instead of showing a spinner
// forever.
import type { TrendPoint } from './types.js';

/** Where the history routes live, and how to reach them. */
export interface HistoryOptions {
	/** Prefix for the request — `''` (same origin, the usual deployment) or an
	 *  absolute runtime URL when the page talks to the runtime directly. */
	base?: string;
	/** Server-side bucket count. The store averages into this many buckets, so
	 *  a 7-day window costs the same as an hour. Default 600. */
	maxPoints?: number;
	/** `X-Nautilus-Token`, for a cross-origin writer/reader. */
	token?: string;
	/** Override the whole path (default `/api/history`). */
	path?: string;
}

/** What `fetchHistory` resolves to. `ok: false` means "no historian", which is
 *  a normal state — the caller renders a notice, not an error. */
export interface HistoryResult {
	ok: boolean;
	/** Tag path → points, oldest first. A tag the historian has never seen
	 *  comes back missing rather than empty; treat both the same. */
	series: Record<string, TrendPoint[]>;
	/** HTTP status, when there was one — 503 is "no historian configured". */
	status?: number;
}

function headers(token?: string): Record<string, string> {
	return token ? { 'X-Nautilus-Token': token } : {};
}

/**
 * Fetch archived points for a set of dotted tag paths over `[fromMs, toMs]`.
 *
 * Struct members archive under their full dotted path — `WEL15_FIT_001.VALUE`
 * — the same name the live frame and every faceplate already use, so a pen
 * needs no translation between live and historical.
 */
export async function fetchHistory(
	tags: string[],
	fromMs: number,
	toMs: number,
	opts: HistoryOptions = {}
): Promise<HistoryResult> {
	if (tags.length === 0) return { ok: true, series: {} };
	const qs = new URLSearchParams({
		tags: tags.join(','),
		from: String(Math.round(fromMs)),
		to: String(Math.round(toMs)),
		maxPoints: String(opts.maxPoints ?? 600)
	});
	try {
		const res = await fetch(`${opts.base ?? ''}${opts.path ?? '/api/history'}?${qs}`, {
			headers: headers(opts.token)
		});
		if (!res.ok) return { ok: false, series: {}, status: res.status };
		const body = (await res.json()) as { series?: Record<string, [number, number][]> };
		const series: Record<string, TrendPoint[]> = {};
		for (const [tag, pts] of Object.entries(body.series ?? {})) {
			series[tag] = pts.map(([t, v]) => ({ t, v }));
		}
		return { ok: true, series };
	} catch {
		return { ok: false, series: {} };
	}
}

/**
 * Is a historian configured and reachable? Probes `GET /api/history/span`,
 * which answers 503 when `server.historian` is unset. Call it once on mount
 * so the page can say so up front rather than after a failed query.
 */
export async function historyAvailable(opts: HistoryOptions = {}): Promise<boolean> {
	try {
		const res = await fetch(`${opts.base ?? ''}/api/history/span`, { headers: headers(opts.token) });
		return res.ok;
	} catch {
		return false;
	}
}

/**
 * Splice archived points onto live ones.
 *
 * The live buffer always wins where the two overlap: it is the raw stream,
 * while the historian's points are bucket-averaged server-side, and showing
 * an averaged value for the last thirty seconds beside a raw one is how a
 * trend ends up with a visible seam. Both inputs must be sorted by time.
 */
export function mergeHistory(historical: TrendPoint[], live: TrendPoint[]): TrendPoint[] {
	if (historical.length === 0) return live;
	if (live.length === 0) return historical;
	const liveFrom = live[0].t;
	return [...historical.filter((p) => p.t < liveFrom), ...live];
}
