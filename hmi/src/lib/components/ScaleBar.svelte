<script lang="ts" module>
	/** One coloured segment of a `ScaleBar`'s scale, in engineering units. */
	export interface ScaleBand {
		from: number;
		to: number;
		/** Any CSS colour. Omit to use the bar's normal-band colour. */
		color?: string;
	}
</script>

<script lang="ts">
	// A banded scale with a moving indicator — the "moving analog indicator"
	// every SCADA package ships: a value read against a scale that has been cut
	// into coloured alarm bands.
	//
	// Bands come in either of two forms, and they compose:
	//
	//   1. alarm setpoints — pass `hh` / `h` / `l` / `ll` as numbers. A limit
	//      that is DISABLED is `null` or `undefined` and simply does not cut the
	//      scale, which is how a UDT's per-limit enable bit is expressed here
	//      (`h={HENB ? HSP : null}`). With no limits enabled the whole scale is
	//      the normal band.
	//   2. explicit `bands` — an arbitrary segmentation, for scales whose colour
	//      breaks are not alarm limits (a pH range, a duty band).
	//
	// Colours are CSS custom properties so a legacy port can pin them to the
	// source palette without prop drilling: `--bar-normal`, `--bar-warn`,
	// `--bar-trip`, `--bar-outline`, `--bar-track`.
	let {
		value,
		min = 0,
		max = 100,
		label = '',
		units = '',
		precision = 1,
		hh = null,
		h = null,
		l = null,
		ll = null,
		bands,
		orientation = 'vertical',
		thickness = 26,
		length = 200,
		showScale = true,
		indicatorColor = 'var(--bar-indicator, var(--ink))',
		format
	}: {
		/** The reading. `undefined` (or a non-finite number) draws no indicator —
		 *  an absent tag must not read as the bottom of the scale. */
		value?: number;
		min?: number;
		max?: number;
		/** Shown above the bar with the formatted value; omit for a bare bar. */
		label?: string;
		units?: string;
		precision?: number;
		/** High-high trip limit; `null`/`undefined` = disabled, no band. */
		hh?: number | null;
		/** High warning limit; `null`/`undefined` = disabled. */
		h?: number | null;
		/** Low warning limit; `null`/`undefined` = disabled. */
		l?: number | null;
		/** Low-low trip limit; `null`/`undefined` = disabled. */
		ll?: number | null;
		/** Explicit segmentation, instead of (or as well as) the limits above. */
		bands?: ScaleBand[];
		orientation?: 'vertical' | 'horizontal';
		/** Across the bar, px. */
		thickness?: number;
		/** Along the bar, px. */
		length?: number;
		showScale?: boolean;
		/** Colour of the moving indicator — drive it from alarm state. */
		indicatorColor?: string;
		/** Override the readout formatting (defaults to `toFixed(precision)`). */
		format?: (v: number) => string;
	} = $props();

	const NORMAL = 'var(--bar-normal, color-mix(in srgb, var(--s1) 30%, var(--surface)))';
	const WARN = 'var(--bar-warn, color-mix(in srgb, var(--warn) 55%, var(--surface)))';
	const TRIP = 'var(--bar-trip, color-mix(in srgb, var(--crit) 55%, var(--surface)))';

	const lo = $derived(min);
	const hi = $derived(max > min ? max : min + 1);
	const span = $derived(hi - lo);

	const pct = (v: number) => Math.max(0, Math.min(100, ((v - lo) / span) * 100));

	const present = $derived(typeof value === 'number' && Number.isFinite(value));

	const segments = $derived.by(() => {
		if (bands?.length) {
			return bands.map((b) => ({
				from: Math.max(lo, Math.min(hi, b.from)),
				to: Math.max(lo, Math.min(hi, b.to)),
				color: b.color ?? NORMAL
			}));
		}
		// Cut points, low → high. Each cut ends the band that runs up to it.
		const cuts: { at: number; color: string }[] = [];
		if (ll != null) cuts.push({ at: ll, color: TRIP });
		if (l != null) cuts.push({ at: l, color: WARN });
		cuts.push({ at: h != null ? h : hh != null ? hh : hi, color: NORMAL });
		if (h != null) cuts.push({ at: hh != null ? hh : hi, color: WARN });
		if (hh != null) cuts.push({ at: hi, color: TRIP });

		const out: { from: number; to: number; color: string }[] = [];
		let from = lo;
		for (const c of cuts) {
			const to = Math.max(from, Math.min(hi, c.at));
			if (to > from) out.push({ from, to, color: c.color });
			from = to;
		}
		if (from < hi) out.push({ from, to: hi, color: NORMAL });
		return out;
	});

	const at = $derived(present ? pct(value as number) : 0);
	const vertical = $derived(orientation === 'vertical');
	const fmt = $derived(format ?? ((v: number) => v.toFixed(precision)));
	const tick = (v: number) => (format ? format(v) : v.toFixed(0));
</script>

<div class="bar" class:vert={vertical}>
	{#if label}
		<div class="head">
			<span class="k">{label}</span>
			<span class="v">{present ? fmt(value as number) : '—'}{units ? ` ${units}` : ''}</span>
		</div>
	{/if}

	<div class="wrap" style:--across={`${thickness}px`} style:--along={`${length}px`}>
		<div
			class="track"
			role="meter"
			aria-valuenow={present ? value : undefined}
			aria-valuemin={lo}
			aria-valuemax={hi}
			aria-label={label || 'scale'}
		>
			{#each segments as b, i (i)}
				<span
					class="band"
					style:background={b.color}
					style:--from={`${pct(b.from)}%`}
					style:--size={`${pct(b.to) - pct(b.from)}%`}
				></span>
			{/each}
			{#if present}
				<span class="needle" style:--at={`${at}%`} style:background={indicatorColor}></span>
			{/if}
		</div>

		{#if showScale}
			<div class="scale">
				<span>{tick(vertical ? hi : lo)}</span><span>{tick(vertical ? lo : hi)}</span>
			</div>
		{/if}
	</div>
</div>

<style>
	.bar {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		width: 100%;
	}

	.head {
		display: flex;
		justify-content: space-between;
		gap: 8px;
		font-size: var(--font-2xs);
		color: var(--muted);
	}

	.head .v {
		font-family: var(--mono);
		color: var(--ink);
		font-weight: 600;
		white-space: nowrap;
	}

	.wrap {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	/* Vertical: the scale sits beside the track, high at the top. */
	.bar.vert .wrap {
		flex-direction: row;
		gap: 5px;
		justify-content: center;
	}

	.track {
		position: relative;
		max-width: 100%;
		background: var(--bar-track, var(--surface-2));
		border: 1px solid var(--bar-outline, var(--axis));
		border-radius: 5px;
		overflow: hidden;
		width: var(--along);
		height: var(--across);
	}

	.bar.vert .track {
		width: var(--across);
		height: var(--along);
		align-self: center;
	}

	.band {
		position: absolute;
		left: var(--from);
		width: var(--size);
		top: 0;
		bottom: 0;
	}

	.bar.vert .band {
		left: 0;
		right: 0;
		width: auto;
		bottom: var(--from);
		height: var(--size);
		top: auto;
	}

	.needle {
		position: absolute;
		top: 0;
		bottom: 0;
		left: var(--at);
		width: 5px;
		margin-left: -2.5px;
		box-shadow: 0 0 0 1px var(--bar-outline, var(--axis));
		transition: left 0.5s ease;
	}

	.bar.vert .needle {
		left: 0;
		right: 0;
		top: auto;
		width: auto;
		height: 5px;
		bottom: var(--at);
		margin-left: 0;
		margin-bottom: -2.5px;
		transition: bottom 0.5s ease;
	}

	.scale {
		display: flex;
		justify-content: space-between;
		font-size: 9px;
		color: var(--muted);
		font-family: var(--mono);
	}

	.bar.vert .scale {
		flex-direction: column;
		align-items: flex-start;
		height: var(--along);
	}
</style>
