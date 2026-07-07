<script lang="ts">
	// A pipe segment drawn from an SVG path. When flowing, a dashed overlay
	// marches along the path; speed scales with rate (0–1).
	// Render inside an <svg> element.
	let {
		d,
		flowing = false,
		rate = 1,
		color = 'var(--s1, #3987e5)'
	}: { d: string; flowing?: boolean; rate?: number; color?: string } = $props();

	// Quantize rate into quarter-steps: a continuously varying duration would
	// restart the CSS animation on every data frame and read as jitter.
	let bucket = $derived(Math.max(0.25, Math.ceil(Math.min(rate, 1) * 4) / 4));
	let period = $derived(flowing && rate > 0.02 ? 0.9 / bucket : 0);
</script>

<g>
	<path {d} fill="none" stroke="var(--axis, #383835)" stroke-width="10" stroke-linecap="round" />
	<path {d} fill="none" stroke="var(--bg, #0d0d0d)" stroke-width="6" stroke-linecap="round" />
	{#if flowing && period > 0}
		<path
			class="flow"
			style="--period: {period}s"
			{d}
			fill="none"
			stroke={color}
			stroke-width="4"
			stroke-linecap="round"
			stroke-dasharray="7 9"
		/>
	{/if}
</g>

<style>
	.flow {
		animation: march var(--period) linear infinite;
	}
	@keyframes march {
		to {
			stroke-dashoffset: -16;
		}
	}
</style>
