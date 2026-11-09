<script lang="ts">
	// Swing check valve: purely mechanical, so no actuator, no position and
	// no signal. Drawn as the P&ID check valve: a solid triangle pointing the
	// way flow may go, its point against a bar that stops it coming back,
	// the pipe meeting it on both sides. It draws in the same box as the
	// kit's Valve at the same width (90 x 80 units, the line 55 % of the way
	// down), so swapping one for the other leaves the pipes where they were.
	// App-supplied via <Mimic registry>, not part of the kit.
	let { label = '', width = 90 }: { label?: string; width?: number } = $props();

	const k = $derived(width / 90);
	const h = $derived(80 * k);
	// Rows as the kit's valve: 28 above the body, 20 for the label.
	const top = $derived(28 * k);
	const bot = $derived(h - 20 * k);
	const cy = $derived((top + bot) / 2);
	const cx = $derived(width / 2);
	const s = $derived(Math.max(1, 2 * k));
	const x0 = $derived(s / 2);
	// The bar at the outlet edge, where the outlet pipe meets it.
	const bar = $derived(width - Math.max(1.5, 3 * k) / 2);
</script>

<svg viewBox="0 0 {width} {h}" {width} height={h} role="img" aria-label={`Check valve ${label}, flow left to right only`}>
	<!-- over the pipes' rounded ends, which reach a little inside the box -->
	<rect x="0" y={top} width={width} height={bot - top} fill="var(--bg, #0d0d0d)" />
	<!-- the triangle points downstream, its point on the bar -->
	<path d="M {x0} {top} L {bar} {cy} L {x0} {bot} Z" fill="var(--ink-2, #c3c2b7)" stroke="var(--ink-2, #c3c2b7)" stroke-width={s} stroke-linejoin="round" />
	<line x1={bar} y1={top} x2={bar} y2={bot} stroke="var(--ink-2, #c3c2b7)" stroke-width={Math.max(1.5, 3 * k)} />
	{#if label}
		<text x={cx} y={h - 4 * k} text-anchor="middle" font-size={Math.max(8, 12 * k)} font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
	}
</style>
