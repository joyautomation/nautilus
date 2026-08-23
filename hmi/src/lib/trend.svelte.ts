// `useTrend` — one tag's trend, live off the SSE stream and backfilled from
// the historian, as a single call inside a component.
//
// The two halves are the point. The SSE stream gives you the freshest data at
// full scan resolution but only from the moment the page opened; the historian
// gives you everything before that, bucket-averaged server-side so a seven-day
// window costs the same as an hour. Neither alone is a trend. `useTrend` keeps
// a capped live buffer, backfills the rest on mount and on every stream
// (re)open — which is exactly when a gap appeared — and hands back one
// reactive `TrendBuffer` with the two spliced together.
//
// Buffers are SHARED and refcounted per (client, tag, window): ten sparklines
// on the same tag cost one subscription and one array, not ten. The last
// component to unmount releases it.
import { TrendBuffer, type RealtimeClient } from './realtime.svelte.js';
import { fetchHistory, type HistoryOptions } from './history.js';
import { numAt, type TagTree } from './tags.js';
import type { TrendPoint } from './types.js';

/** Options for `useTrend`. */
export interface UseTrendOptions<F> {
	/**
	 * Seconds of history the buffer holds. Also the span backfilled from the
	 * historian. Default 900 (15 min).
	 *
	 * Keep this modest for a LIVE buffer: `TrendBuffer.push` re-filters the
	 * whole array on every sample, so a literal seven-day window fed off raw
	 * SSE ticks grows without bound. For a wide window, cap the live buffer
	 * here and splice a separate historian query on with `mergeHistory`.
	 */
	windowS?: number;
	/** Pull the value for `tag` out of a frame. Defaults to a dotted-path
	 *  lookup against `frame.tags`. */
	read?: (frame: F, tag: string) => number;
	/** The frame's timestamp, epoch ms. Defaults to `frame.ts`, then `now`. */
	ts?: (frame: F) => number;
	/**
	 * Backfill from `GET /api/history` on mount and on every stream (re)open.
	 * Off by default — a runtime with no historian would 503 on every trend.
	 */
	history?: boolean;
	/** Where the history routes live (base URL, token, bucket count). */
	historyOptions?: HistoryOptions;
	/** The name to ask the historian for, if it differs from `tag` (a site
	 *  runtime serves `WEL15_FIT_001.VALUE` for `RTU9_WEL15_FIT_001.VALUE`). */
	historyTag?: string;
	/** Also re-backfill on this interval, ms. 0 (default) = only on open. */
	refreshMs?: number;
}

interface Entry {
	buf: TrendBuffer;
	refs: number;
	release: () => void;
}

// Per-client registries, so two runtimes on one page never share a buffer.
const registries = new WeakMap<object, Map<string, Entry>>();

function registryFor(client: object): Map<string, Entry> {
	let m = registries.get(client);
	if (!m) {
		m = new Map();
		registries.set(client, m);
	}
	return m;
}

function defaultRead<F>(frame: F, tag: string): number {
	const tags = (frame as { tags?: TagTree } | null)?.tags;
	return numAt(tags, tag);
}

function defaultTs<F>(frame: F): number {
	const t = (frame as { ts?: number } | null)?.ts;
	return typeof t === 'number' && t > 0 ? t : Date.now();
}

/**
 * Subscribe to a tag's trend for as long as the calling component lives. Call
 * it during component initialisation; the returned buffer's `.points` is
 * reactive.
 *
 * ```svelte
 * <script>
 *   const flow = useTrend(rt.realtime, 'WEL15_FIT_001.VALUE', { history: true });
 * </script>
 * <Sparkline values={flow.points.map((p) => p.v)} />
 * ```
 */
export function useTrend<F = unknown>(
	client: RealtimeClient<F>,
	tag: string,
	opts: UseTrendOptions<F> = {}
): TrendBuffer {
	const windowS = opts.windowS ?? 900;
	const key = `${tag}@${windowS}@${opts.history ? 1 : 0}`;
	const reg = registryFor(client as object);

	let entry = reg.get(key);
	if (!entry) {
		const buf = new TrendBuffer(windowS);
		const read = opts.read ?? defaultRead<F>;
		const ts = opts.ts ?? defaultTs<F>;

		const offFrame = client.onFrame((f) => {
			const v = read(f, tag);
			if (Number.isFinite(v)) buf.push(ts(f), v);
		});

		let offOpen: (() => void) | undefined;
		let timer: ReturnType<typeof setInterval> | undefined;

		if (opts.history) {
			const name = opts.historyTag ?? tag;
			const backfill = async () => {
				const to = Date.now();
				const res = await fetchHistory([name], to - windowS * 1000, to, opts.historyOptions);
				const pts: TrendPoint[] = res.series[name] ?? [];
				if (pts.length) buf.seed(pts);
			};
			void backfill();
			offOpen = client.onOpen(() => void backfill());
			if (opts.refreshMs && opts.refreshMs > 0) {
				timer = setInterval(() => void backfill(), opts.refreshMs);
			}
		}

		entry = {
			buf,
			refs: 0,
			release: () => {
				offFrame();
				offOpen?.();
				if (timer) clearInterval(timer);
			}
		};
		reg.set(key, entry);
	}

	entry.refs++;
	const e = entry;
	$effect(() => {
		return () => {
			e.refs--;
			if (e.refs <= 0) {
				e.release();
				reg.delete(key);
			}
		};
	});
	return entry.buf;
}
