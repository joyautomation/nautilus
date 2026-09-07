<script lang="ts">
	// Full-featured multi-pen trend chart. Complements Trend.svelte (which
	// keeps its small `series[]` shape and existing consumers) rather than
	// replacing it — this one is for when you need many pens, mixed units,
	// alarm bands, and legend-as-readout in one chart. SVG only, no charting
	// library, same lineage as Trend/Sparkline/Histogram.
	//
	// Live vs. historical isn't a mode switch: a pen's `data` can be a static
	// TrendPoint[] or a TrendBuffer's reactive `.points` (push straight into
	// the buffer from a RealtimeClient's `onFrame` and the chart just follows
	// it). Pass `windowMs` for a rolling live window; omit it to auto-fit the
	// full extent of the data (historical/static).
	//
	// Scaling never goes dual-axis (two y-scales invite misreading a chart).
	// `axisMode="shared"` puts every pen on one auto/fixed engineering-unit
	// axis — use it when pens share units. `axisMode="percent"` normalizes
	// each pen to its own min/max and shares a single 0–100% axis instead —
	// use it for pens with wildly different units (e.g. °C next to a valve
	// %). Readouts (legend + hover) always show the pen's real value in its
	// own units; only the *geometry* changes between modes.
	import Icon from './Icon.svelte';
	import { isThresholdActive, resolveYLabel } from '../trendchart.js';
	import type { TrendPen, TrendThreshold } from '../types.js';

	let {
		pens,
		thresholds = [],
		height = 260,
		windowMs,
		axisMode = 'shared',
		yMin,
		yMax,
		yLabel,
		gapMs,
		paused = $bindable(false)
	}: {
		pens: TrendPen[];
		/** A pen's own threshold (`penId` set) or a CHART-LEVEL threshold
		 *  (`penId` omitted) drawn against the shared axis — only meaningful
		 *  with `axisMode="shared"`, silently skipped in `percent` mode. */
		thresholds?: TrendThreshold[];
		height?: number;
		/** Rolling live window in ms. Omit to auto-fit the full data extent (historical). */
		windowMs?: number;
		/** 'shared': one engineering-units axis (best when pens share units).
		 *  'percent': each pen normalized to its own range, shared 0–100% axis. */
		axisMode?: 'shared' | 'percent';
		/** Shared-mode fixed bounds; omit either to auto-scale that side. */
		yMin?: number;
		yMax?: number;
		/** Explicit y-axis label, overriding auto-detection from the pens'
		 *  shared `units` — needed when pens carry mixed units, or whenever a
		 *  chart (e.g. a report) must always carry the correct engineering-unit
		 *  label. Only applies in `shared` mode; `percent` mode always shows
		 *  "% of range". */
		yLabel?: string;
		/** Break a pen's line where consecutive samples are farther apart than this, ms. */
		gapMs?: number;
		/** Freeze the visible window while data keeps accumulating (live mode). */
		paused?: boolean;
	} = $props();

	// Default categorical palette: the kit's validated series tokens
	// (theme.css), in a fixed order — never cycled into a fresh hue. A 5th+
	// pen reuses the same four colors with a distinct dash so identity never
	// rests on hue alone (colorblind-safe per the dataviz house rules).
	const PALETTE = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s5)'];
	const CYCLE_DASH = ['none', '7 4', '2 3', '1 5 4 5'];

	const PAD = { l: 54, r: 16, t: 14, b: 24 };

	let w = $state(640);

	// Per-pen visibility (legend click to hide/show). Seeded once from
	// `pen.hidden`; toggling afterwards is purely local UI state, so reading
	// `pens` only at init (not reactively) is intentional.
	// svelte-ignore state_referenced_locally
	let hiddenIds = $state<Set<string>>(new Set(pens.filter((p) => p.hidden).map((p) => p.id)));
	function toggle(id: string) {
		const next = new Set(hiddenIds);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		hiddenIds = next;
	}

	interface StyledPen extends TrendPen {
		color: string;
		dash: string;
		visible: boolean;
	}

	const styled: StyledPen[] = $derived(
		pens.map((p, i) => ({
			...p,
			color: p.color ?? PALETTE[i % PALETTE.length],
			dash: p.dashed ? '6 5' : CYCLE_DASH[Math.floor(i / PALETTE.length) % CYCLE_DASH.length],
			visible: !hiddenIds.has(p.id)
		}))
	);
	const visible = $derived(styled.filter((p) => p.visible));
	const hasAnyData = $derived(pens.some((p) => p.data.length > 0));

	// Time domain. `latest`/`earliest` scan *all* pens (hidden ones too, so
	// toggling visibility never shifts the window). `windowEnd` freezes
	// while paused: the effect only reads `latest` on the branch it takes,
	// so once paused it stops resubscribing to it and simply holds still —
	// no separate "frozen timestamp" bookkeeping needed.
	const latest = $derived.by(() => {
		let m = 0;
		for (const p of pens) for (const d of p.data) if (d.t > m) m = d.t;
		return m;
	});
	const earliest = $derived.by(() => {
		let m = Infinity;
		for (const p of pens) for (const d of p.data) if (d.t < m) m = d.t;
		return m;
	});
	let windowEnd = $state(0);
	$effect(() => {
		if (!paused) windowEnd = latest;
	});

	const t1 = $derived(windowEnd || latest);
	const t0 = $derived(
		windowMs !== undefined ? t1 - windowMs : isFinite(earliest) ? earliest : t1 - 60_000
	);
	const spanMs = $derived(Math.max(t1 - t0, 1));

	function togglePause() {
		paused = !paused;
	}

	// ── Value axis: "nice" ticks (Heckbert's algorithm) ─────────────────────
	function niceNumber(range: number, round: boolean): number {
		if (range <= 0) return 1;
		const exp = Math.floor(Math.log10(range));
		const frac = range / 10 ** exp;
		let niceFrac: number;
		if (round) niceFrac = frac < 1.5 ? 1 : frac < 3 ? 2 : frac < 7 ? 5 : 10;
		else niceFrac = frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 5 ? 5 : 10;
		return niceFrac * 10 ** exp;
	}

	function niceTicks(lo: number, hi: number, count = 5) {
		if (!isFinite(lo) || !isFinite(hi) || hi - lo < 1e-9) {
			const v = isFinite(lo) ? lo : 0;
			return { min: v - 1, max: v + 1, ticks: [v - 1, v, v + 1] };
		}
		const step = niceNumber(niceNumber(hi - lo, false) / Math.max(count - 1, 1), true);
		const min = Math.floor(lo / step) * step;
		const max = Math.ceil(hi / step) * step;
		const ticks: number[] = [];
		for (let v = min; v <= max + step * 0.5; v += step) ticks.push(Math.round(v / step) * step);
		return { min, max, ticks };
	}

	// Shared-mode domain: union of the visible pens' in-window data (or the
	// fixed yMin/yMax override, either side independently).
	const sharedDomain = $derived.by(() => {
		let lo = yMin ?? Infinity;
		let hi = yMax ?? -Infinity;
		if (yMin === undefined || yMax === undefined) {
			for (const p of visible)
				for (const d of p.data) {
					if (d.t < t0 || d.t > t1) continue;
					if (yMin === undefined && d.v < lo) lo = d.v;
					if (yMax === undefined && d.v > hi) hi = d.v;
				}
		}
		if (!isFinite(lo)) lo = 0;
		if (!isFinite(hi)) hi = lo + 1;
		return niceTicks(lo, hi);
	});

	// Percent-mode: each pen gets its own in-window (or fixed min/max) range.
	const penRanges = $derived.by(() => {
		const map = new Map<string, { lo: number; hi: number }>();
		for (const p of styled) {
			let lo = p.min ?? Infinity;
			let hi = p.max ?? -Infinity;
			if (p.min === undefined || p.max === undefined) {
				for (const d of p.data) {
					if (d.t < t0 || d.t > t1) continue;
					if (p.min === undefined && d.v < lo) lo = d.v;
					if (p.max === undefined && d.v > hi) hi = d.v;
				}
			}
			if (!isFinite(lo)) lo = 0;
			if (!isFinite(hi) || hi <= lo) hi = lo + 1;
			map.set(p.id, { lo, hi });
		}
		return map;
	});
	const percentDomain = niceTicks(0, 100);

	const commonUnit = $derived.by(() => {
		const units = new Set(visible.map((p) => p.units ?? ''));
		return units.size === 1 ? [...units][0] : '';
	});

	// ── Scales ───────────────────────────────────────────────────────────────
	let xOf = $derived((t: number) => PAD.l + ((t - t0) / spanMs) * (w - PAD.l - PAD.r));

	function yFromDomain(v: number, lo: number, hi: number) {
		return PAD.t + (1 - (v - lo) / (hi - lo || 1)) * (height - PAD.t - PAD.b);
	}
	let yOf = $derived.by(() => {
		if (axisMode === 'percent') {
			return (pen: StyledPen, v: number) => {
				const r = penRanges.get(pen.id) ?? { lo: 0, hi: 1 };
				const pct = ((v - r.lo) / (r.hi - r.lo || 1)) * 100;
				return yFromDomain(pct, 0, 100);
			};
		}
		const d = sharedDomain;
		return (_pen: StyledPen, v: number) => yFromDomain(v, d.min, d.max);
	});

	let yTicks = $derived.by(() => {
		const d = axisMode === 'percent' ? percentDomain : sharedDomain;
		return d.ticks
			.filter((v) => v >= d.min - 1e-9 && v <= d.max + 1e-9)
			.map((v) => ({ v, y: yFromDomain(v, d.min, d.max) }));
	});

	let xTicks = $derived.by(() => {
		const n = 5;
		return Array.from({ length: n + 1 }, (_, i) => {
			const t = t0 + (i / n) * spanMs;
			return { t, x: xOf(t) };
		});
	});

	// ── Paths (with optional gap breaks) & single-point markers ─────────────
	const paths = $derived(
		visible.map((p) => {
			const pts = p.data
				.filter((d) => d.t >= t0 && d.t <= t1)
				.slice()
				.sort((a, b) => a.t - b.t);
			let d = '';
			let prevT: number | null = null;
			for (const pt of pts) {
				const brk = gapMs !== undefined && prevT !== null && pt.t - prevT > gapMs;
				d += `${d === '' || brk ? 'M' : 'L'} ${xOf(pt.t).toFixed(1)} ${yOf(p, pt.v).toFixed(1)} `;
				prevT = pt.t;
			}
			return { pen: p, d: d.trim(), single: pts.length === 1 ? pts[0] : null };
		})
	);

	function fmtValue(v: number) {
		return Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 10 ? v.toFixed(1) : v.toFixed(2);
	}

	function fmtTime(t: number) {
		const d = new Date(t);
		const p2 = (n: number) => String(n).padStart(2, '0');
		if (spanMs <= 120_000) return `${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
		if (spanMs <= 6 * 3_600_000) return `${p2(d.getHours())}:${p2(d.getMinutes())}`;
		if (spanMs <= 4 * 86_400_000)
			return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
		return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}

	// ── Hover: nearest sample per visible pen at the cursor's time ──────────
	let cursor = $state<number | null>(null); // pointer x in svg viewBox units
	function onmove(ev: PointerEvent) {
		const rect = (ev.currentTarget as SVGElement).getBoundingClientRect();
		cursor = ((ev.clientX - rect.left) / rect.width) * w;
	}
	function onleave() {
		cursor = null;
	}

	const hoverT = $derived(
		cursor !== null && cursor >= PAD.l && cursor <= w - PAD.r
			? t0 + ((cursor - PAD.l) / (w - PAD.l - PAD.r)) * spanMs
			: null
	);
	const hoverX = $derived(hoverT !== null ? xOf(hoverT) : null);

	function nearest(pen: StyledPen, t: number) {
		let best: { t: number; v: number } | null = null;
		for (const d of pen.data) {
			if (d.t < t0 || d.t > t1) continue;
			if (!best || Math.abs(d.t - t) < Math.abs(best.t - t)) best = d;
		}
		return best;
	}

	// Legend/readout value: the sample at the hover time (when hovering a
	// visible pen), otherwise the sample at the window's right edge — which
	// is the true latest sample while live, and a stable frozen one while
	// paused. Never flickers: same lookup either way.
	function legendValue(p: StyledPen) {
		if (hoverT !== null && p.visible) return nearest(p, hoverT);
		return nearest(p, t1);
	}

	const visiblePenIds = $derived(new Set(visible.map((p) => p.id)));
	const activeThresholds = $derived(
		thresholds.filter((th) => isThresholdActive(th, axisMode, visiblePenIds))
	);
	const yLabelText = $derived(resolveYLabel(axisMode, yLabel, commonUnit));

	// A per-pen threshold reads its `value`/`lo`/`hi` through that pen's own
	// scale (`yOf`, which handles percent-mode normalization); a chart-level
	// threshold (`penId` omitted — only ever active in `shared` mode, per
	// `isThresholdActive`) reads straight off the shared domain instead, since
	// it isn't tied to any one pen's range. `undefined` means "don't draw" —
	// e.g. a per-pen threshold whose pen got hidden after `activeThresholds`
	// was computed on a stale value.
	function yForThreshold(th: TrendThreshold, v: number): number | undefined {
		if (th.penId === undefined) return yFromDomain(v, sharedDomain.min, sharedDomain.max);
		const pen = visible.find((p) => p.id === th.penId);
		return pen ? yOf(pen, v) : undefined;
	}
</script>

<div class="trendchart" bind:clientWidth={w}>
	<div class="head">
		<div class="legend">
			{#each styled as p (p.id)}
				{@const sample = legendValue(p)}
				<button
					type="button"
					class="pen"
					class:hidden={!p.visible}
					onclick={() => toggle(p.id)}
					aria-pressed={p.visible}
					title={p.visible ? `Hide ${p.label}` : `Show ${p.label}`}
				>
					<span class="swatch" style="background: {p.color}"></span>
					<span class="lbl">{p.label}</span>
					<b class="val num">{sample ? fmtValue(sample.v) : '—'}{p.units ?? ''}</b>
				</button>
			{/each}
		</div>
		{#if windowMs !== undefined}
			<button
				type="button"
				class="pausebtn"
				onclick={togglePause}
				aria-pressed={paused}
				title={paused ? 'Resume' : 'Pause'}
			>
				<Icon name={paused ? 'play' : 'pause'} size={14} />
				{paused ? 'Paused' : 'Live'}
			</button>
		{/if}
	</div>

	<svg
		viewBox="0 0 {w} {height}"
		style="width: 100%; display: block"
		onpointermove={onmove}
		onpointerleave={onleave}
		role="img"
		aria-label={`Trend: ${styled.map((p) => p.label).join(', ')}`}
	>
		{#if !hasAnyData}
			<text x={w / 2} y={height / 2} text-anchor="middle" font-size="12" fill="var(--muted)">
				No data
			</text>
		{:else}
			{#each yTicks as tk}
				<line x1={PAD.l} x2={w - PAD.r} y1={tk.y} y2={tk.y} stroke="var(--grid)" stroke-width="1" />
				<text x={PAD.l - 6} y={tk.y + 3.5} text-anchor="end" font-size="12" fill="var(--muted)" class="num">
					{axisMode === 'percent' ? tk.v.toFixed(0) : fmtValue(tk.v)}
				</text>
			{/each}
			{#each xTicks as tk}
				<text x={tk.x} y={height - 8} text-anchor="middle" font-size="12" fill="var(--muted)" class="num">
					{fmtTime(tk.t)}
				</text>
			{/each}
			{#if yLabelText}
				<text x={PAD.l} y={PAD.t - 3} font-size="12" fill="var(--muted)">{yLabelText}</text>
			{/if}

			<!-- Alarm bands, drawn behind everything else. -->
			{#each activeThresholds.filter((th) => th.lo !== undefined && th.hi !== undefined) as th}
				{@const yA = yForThreshold(th, th.hi as number)}
				{@const yB = yForThreshold(th, th.lo as number)}
				{#if yA !== undefined && yB !== undefined}
					<rect
						x={PAD.l}
						y={Math.min(yA, yB)}
						width={w - PAD.l - PAD.r}
						height={Math.max(Math.abs(yB - yA), 1)}
						fill={`var(--${th.kind})`}
						fill-opacity="0.1"
					/>
				{/if}
			{/each}

			<line x1={PAD.l} x2={w - PAD.r} y1={height - PAD.b} y2={height - PAD.b} stroke="var(--axis)" stroke-width="1" />

			<!-- Threshold lines, above the bands but below the data. -->
			{#each activeThresholds.filter((th) => th.value !== undefined) as th}
				{@const y = yForThreshold(th, th.value as number)}
				{#if y !== undefined}
					<line x1={PAD.l} x2={w - PAD.r} y1={y} y2={y} stroke={`var(--${th.kind})`} stroke-width="1.5" stroke-dasharray="4 3" opacity="0.85" />
					{#if th.label}
						<text x={w - PAD.r - 4} y={y - 4} text-anchor="end" font-size="12" fill={`var(--${th.kind})`}>{th.label}</text>
					{/if}
				{/if}
			{/each}

			{#each paths as sp (sp.pen.id)}
				{#if sp.d}
					<path d={sp.d} fill="none" stroke={sp.pen.color} stroke-width="2" stroke-dasharray={sp.pen.dash === 'none' ? 'none' : sp.pen.dash} stroke-linecap="round" stroke-linejoin="round" />
				{/if}
				{#if sp.single}
					<circle cx={xOf(sp.single.t)} cy={yOf(sp.pen, sp.single.v)} r="3.5" fill={sp.pen.color} />
				{/if}
			{/each}

			{#if hoverX !== null}
				<line x1={hoverX} x2={hoverX} y1={PAD.t} y2={height - PAD.b} stroke="var(--ink-2)" stroke-width="1" stroke-dasharray="3 3" opacity="0.6" />
				<text
					x={Math.min(Math.max(hoverX, PAD.l + 24), w - PAD.r - 24)}
					y={height - PAD.b + 15}
					text-anchor="middle"
					font-size="12"
					fill="var(--ink-2)"
					class="num"
				>
					{hoverT !== null ? fmtTime(hoverT) : ''}
				</text>
			{/if}
		{/if}
	</svg>
</div>

<style>
	.trendchart {
		width: 100%;
	}
	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 10px;
		margin-bottom: 6px;
	}
	.legend {
		display: flex;
		gap: 4px;
		flex-wrap: wrap;
	}
	.pen {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		border: 1px solid transparent;
		background: none;
		border-radius: 7px;
		padding: 3px 7px;
		font: inherit;
		font-size: var(--font-2xs);
		color: var(--ink-2);
		cursor: pointer;
	}
	.pen:hover {
		background: var(--hover);
	}
	.pen:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.pen.hidden {
		opacity: 0.4;
	}
	.pen .swatch {
		width: 10px;
		height: 10px;
		border-radius: 3px;
		flex: none;
	}
	.pen .val {
		color: var(--ink);
		font-weight: 650;
	}
	.pausebtn {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		flex: none;
		border: 1px solid var(--border);
		background: var(--surface-2);
		border-radius: 7px;
		padding: 4px 9px;
		font: inherit;
		font-size: var(--font-2xs);
		font-weight: 600;
		color: var(--ink-2);
		cursor: pointer;
	}
	.pausebtn:hover {
		background: var(--hover);
		color: var(--ink);
	}
	.pausebtn[aria-pressed='true'] {
		border-color: var(--warn);
		color: var(--warn);
	}
	.pausebtn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
</style>
