<script lang="ts">
	// Realtime trend chart: multi-series line, one y-axis (never dual-axis),
	// crosshair + tooltip, legend with live values.
	import type { TrendPoint } from '../types.js';

	interface Series {
		name: string;
		color: string;
		points: TrendPoint[];
		dashed?: boolean;
	}

	let {
		series,
		unit = '',
		height = 220,
		yMin,
		yMax,
		windowS = 180
	}: {
		series: Series[];
		unit?: string;
		height?: number;
		yMin?: number;
		yMax?: number;
		windowS?: number;
	} = $props();

	const PAD = { l: 46, r: 14, t: 10, b: 24 };
	let w = $state(640);
	let cursor = $state<{ px: number; py: number } | null>(null);

	let now = $derived(Math.max(0, ...series.map((s) => s.points.at(-1)?.t ?? 0)));
	let t0 = $derived(now - windowS * 1000);

	let visible = $derived(
		series.map((s) => ({ ...s, pts: s.points.filter((p) => p.t >= t0) }))
	);

	let dom = $derived.by(() => {
		let lo = yMin ?? Infinity;
		let hi = yMax ?? -Infinity;
		if (yMin === undefined || yMax === undefined) {
			for (const s of visible)
				for (const p of s.pts) {
					if (yMin === undefined && p.v < lo) lo = p.v;
					if (yMax === undefined && p.v > hi) hi = p.v;
				}
			if (!isFinite(lo)) lo = 0;
			if (!isFinite(hi)) hi = 1;
			const pad = (hi - lo) * 0.12 || 1;
			if (yMin === undefined) lo -= pad;
			if (yMax === undefined) hi += pad;
		}
		return { lo, hi };
	});

	let px = $derived((t: number) => PAD.l + ((t - t0) / (windowS * 1000)) * (w - PAD.l - PAD.r));
	let py = $derived(
		(v: number) => PAD.t + (1 - (v - dom.lo) / (dom.hi - dom.lo || 1)) * (height - PAD.t - PAD.b)
	);

	let paths = $derived(
		visible.map((s) => ({
			...s,
			d: s.pts.map((p, i) => `${i ? 'L' : 'M'} ${px(p.t).toFixed(1)} ${py(p.v).toFixed(1)}`).join(' ')
		}))
	);

	let yTicks = $derived(
		[0, 0.25, 0.5, 0.75, 1].map((f) => ({
			v: dom.lo + f * (dom.hi - dom.lo),
			y: py(dom.lo + f * (dom.hi - dom.lo))
		}))
	);

	let xTicks = $derived.by(() => {
		const n = 4;
		return Array.from({ length: n + 1 }, (_, i) => {
			const t = t0 + (i / n) * windowS * 1000;
			return { t, x: px(t) };
		});
	});

	function fmtClock(t: number) {
		const d = new Date(t);
		return `${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`;
	}

	function fmt(v: number) {
		return Math.abs(v) >= 100 ? v.toFixed(0) : v.toFixed(1);
	}

	// Crosshair: nearest sample per series at the cursor's time.
	let hover = $derived.by(() => {
		if (!cursor || cursor.px < PAD.l || cursor.px > w - PAD.r) return null;
		const t = t0 + ((cursor.px - PAD.l) / (w - PAD.l - PAD.r)) * windowS * 1000;
		const rows = visible
			.map((s) => {
				let best: TrendPoint | null = null;
				for (const p of s.pts) {
					if (!best || Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
				}
				return best ? { name: s.name, color: s.color, v: best.v } : null;
			})
			.filter((r) => r !== null);
		return rows.length ? { t, x: px(t), rows } : null;
	});

	let tipX = $derived(hover ? Math.min(hover.x + 10, w - 130) : 0);

	function onmove(ev: PointerEvent) {
		const rect = (ev.currentTarget as SVGElement).getBoundingClientRect();
		cursor = {
			px: ((ev.clientX - rect.left) / rect.width) * w,
			py: ((ev.clientY - rect.top) / rect.height) * height
		};
	}
</script>

<div class="trend" bind:clientWidth={w}>
	{#if series.length >= 2}
		<div class="legend">
			{#each visible as s}
				<span class="item">
					<span class="swatch" style="background: {s.color}"></span>
					{s.name}
					<b class="num">{s.pts.length ? fmt(s.pts.at(-1)!.v) : '—'}{unit}</b>
				</span>
			{/each}
		</div>
	{/if}

	<svg
		viewBox="0 0 {w} {height}"
		style="width: 100%; display: block"
		onpointermove={onmove}
		onpointerleave={() => (cursor = null)}
		role="img"
		aria-label="Trend chart"
	>
		{#each yTicks as tk}
			<line x1={PAD.l} x2={w - PAD.r} y1={tk.y} y2={tk.y} stroke="var(--grid)" stroke-width="1" />
			<text x={PAD.l - 6} y={tk.y + 3.5} text-anchor="end" font-size="10" fill="var(--muted)" class="num">{fmt(tk.v)}</text>
		{/each}
		{#each xTicks as tk}
			<text x={tk.x} y={height - 8} text-anchor="middle" font-size="10" fill="var(--muted)" class="num">{fmtClock(tk.t)}</text>
		{/each}
		<line x1={PAD.l} x2={w - PAD.r} y1={height - PAD.b} y2={height - PAD.b} stroke="var(--axis)" stroke-width="1" />

		{#each paths as s}
			<path d={s.d} fill="none" stroke={s.color} stroke-width="2" stroke-dasharray={s.dashed ? '6 5' : 'none'} stroke-linejoin="round" />
		{/each}

		{#if hover}
			<line x1={hover.x} x2={hover.x} y1={PAD.t} y2={height - PAD.b} stroke="var(--ink-2)" stroke-width="1" stroke-dasharray="3 3" opacity="0.6" />
			<g>
				<rect x={tipX} y={PAD.t + 4} width="120" height={16 + hover.rows.length * 16} rx="6" fill="var(--surface-2)" stroke="var(--border)" />
				<text x={tipX + 8} y={PAD.t + 18} font-size="10" fill="var(--muted)" class="num">{fmtClock(hover.t)}</text>
				{#each hover.rows as r, i}
					<circle cx={tipX + 12} cy={PAD.t + 30 + i * 16} r="3.5" fill={r.color} />
					<text x={tipX + 20} y={PAD.t + 34 + i * 16} font-size="11" fill="var(--ink)">
						{r.name} <tspan font-weight="650" class="num">{fmt(r.v)}{unit}</tspan>
					</text>
				{/each}
			</g>
		{/if}
	</svg>
</div>

<style>
	.legend {
		display: flex;
		gap: 16px;
		flex-wrap: wrap;
		margin-bottom: 6px;
		font-size: 12px;
		color: var(--ink-2);
	}
	.item {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.item b {
		color: var(--ink);
		font-weight: 650;
	}
	.swatch {
		width: 10px;
		height: 10px;
		border-radius: 3px;
		display: inline-block;
	}
</style>
