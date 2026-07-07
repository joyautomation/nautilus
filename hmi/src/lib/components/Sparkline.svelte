<script lang="ts">
	// Minimal single-series line for stat tiles and diagnostics rows.
	let {
		values,
		color = 'var(--s1)',
		height = 44,
		yMin,
		yMax
	}: { values: number[]; color?: string; height?: number; yMin?: number; yMax?: number } =
		$props();

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

	let d = $derived(
		values
			.map((v, i) => {
				const x = (i / Math.max(values.length - 1, 1)) * (w - 4) + 2;
				const y = 3 + (1 - (v - dom.lo) / (dom.hi - dom.lo)) * (height - 6);
				return `${i ? 'L' : 'M'} ${x.toFixed(1)} ${y.toFixed(1)}`;
			})
			.join(' ')
	);
</script>

<div bind:clientWidth={w} style="width: 100%">
	<svg viewBox="0 0 {w} {height}" style="width: 100%; display: block" role="img" aria-label="sparkline">
		{#if values.length > 1}
			<path {d} fill="none" stroke={color} stroke-width="1.5" stroke-linejoin="round" />
		{/if}
	</svg>
</div>
