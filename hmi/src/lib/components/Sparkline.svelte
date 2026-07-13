<script lang="ts">
	// Minimal single-series line for stat tiles and diagnostics rows.
	//
	// Two modes. Static (default): the line is fit to the box and redrawn in
	// place. Scrolling (pass `endIndex`, the absolute sample index of the
	// newest value, e.g. a scan counter): samples are plotted at absolute
	// positions and a transitioned transform slides the window left as new
	// samples arrive, so live series scroll smoothly instead of snapping.
	let {
		values,
		color = 'var(--s1)',
		height = 44,
		yMin,
		yMax,
		endIndex,
		windowSize
	}: {
		values: number[];
		color?: string;
		height?: number;
		yMin?: number;
		yMax?: number;
		/** Absolute index of values[values.length-1]; enables scrolling mode. */
		endIndex?: number;
		/** Samples spanning the full width in scrolling mode (default: values.length). */
		windowSize?: number;
	} = $props();

	let w = $state(240);

	let dom = $derived.by(() => {
		let lo = yMin ?? Math.min(...values);
		let hi = yMax ?? Math.max(...values);
		if (!isFinite(lo) || !isFinite(hi)) return { lo: 0, hi: 1 };
		if (hi - lo < 1e-9) {
			lo -= 0.5;
			hi += 0.5;
		}
		return { lo, hi };
	});

	let scrolling = $derived(endIndex !== undefined);
	let dx = $derived((w - 4) / Math.max((windowSize ?? values.length) - 1, 1));

	let d = $derived(
		values
			.map((v, i) => {
				const x = scrolling
					? ((endIndex as number) - values.length + 1 + i) * dx
					: (i / Math.max(values.length - 1, 1)) * (w - 4) + 2;
				const y = 3 + (1 - (v - dom.lo) / (dom.hi - dom.lo)) * (height - 6);
				return `${i ? 'L' : 'M'} ${x.toFixed(2)} ${y.toFixed(2)}`;
			})
			.join(' ')
	);

	let tx = $derived(scrolling ? w - 2 - (endIndex as number) * dx : 0);
</script>

<div bind:clientWidth={w} style="width: 100%">
	<svg viewBox="0 0 {w} {height}" style="width: 100%; display: block" role="img" aria-label="sparkline">
		{#if values.length > 1}
			<g class:scroll={scrolling} style="transform: translateX({tx.toFixed(2)}px)">
				<path {d} fill="none" stroke={color} stroke-width="1.5" stroke-linejoin="round" />
			</g>
		{/if}
	</svg>
</div>

<style>
	g.scroll {
		transition: transform 0.3s linear;
	}
	@media (prefers-reduced-motion: reduce) {
		g.scroll {
			transition: none;
		}
	}
</style>
