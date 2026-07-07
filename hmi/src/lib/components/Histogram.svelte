<script lang="ts">
	// Distribution bars, single sequential hue, 2px gaps, rounded data-ends.
	let {
		counts,
		bucketWidth = 2,
		unit = 'ms',
		height = 150,
		color = 'var(--primary-hover, #256abf)'
	}: { counts: number[]; bucketWidth?: number; unit?: string; height?: number; color?: string } =
		$props();

	let w = $state(400);
	const PAD = { b: 20, t: 6 };
	let max = $derived(Math.max(...counts, 1));
	let bw = $derived(w / counts.length);
	let total = $derived(counts.reduce((a, c) => a + c, 0));
</script>

<div bind:clientWidth={w} style="width: 100%">
	<svg viewBox="0 0 {w} {height}" style="width: 100%; display: block" role="img" aria-label="Histogram">
		{#each counts as c, i}
			{@const h = (c / max) * (height - PAD.b - PAD.t)}
			<rect
				class="bar"
				x={i * bw + 1}
				y={height - PAD.b - h}
				width={Math.max(bw - 2, 1)}
				height={Math.max(h, c > 0 ? 2 : 0)}
				rx="3"
				fill={color}
			>
				<title>{i * bucketWidth}–{(i + 1) * bucketWidth} {unit}: {c} ({total ? ((c / total) * 100).toFixed(1) : 0}%)</title>
			</rect>
			{#if i % 2 === 0}
				<text x={i * bw} y={height - 6} font-size="10" fill="var(--muted)" class="num">{i * bucketWidth}</text>
			{/if}
		{/each}
		<line x1="0" x2={w} y1={height - PAD.b} y2={height - PAD.b} stroke="var(--axis)" />
		<text x={w - 2} y={height - 6} text-anchor="end" font-size="10" fill="var(--muted)">{unit}</text>
	</svg>
</div>

<style>
	.bar {
		transition:
			y 0.3s linear,
			height 0.3s linear;
	}
</style>
