<script lang="ts">
	// Swing check valve: purely mechanical, so no actuator, no position and
	// no signal. Drawn as the P&ID check valve: a solid triangle pointing the
	// way flow may go, its point against a bar that stops it coming back,
	// the pipe meeting it on both sides. The width sets the size: the
	// triangle is four fifths as tall as it is long, with room above and
	// below for the label, so the line runs through the middle of the box
	// (the ports in CheckValve.component.json sit at half height).
	// App-supplied via <Mimic registry>, not part of the kit.
	let { label = '', width = 26 }: { label?: string; width?: number } = $props();

	const pad = 12;
	const body = $derived(width * 0.8);
	const h = $derived(body + 2 * pad);
	const top = pad;
	const bot = $derived(pad + body);
	const cy = $derived(h / 2);
	const bar = $derived(width - 1.25);
</script>

<svg viewBox="0 0 {width} {h}" {width} height={h} role="img" aria-label={`Check valve ${label}, flow left to right only`}>
	<!-- over the pipes' rounded ends, which reach a little inside the box -->
	<rect x="0" y={top} {width} height={body} fill="var(--bg, #0d0d0d)" />
	<!-- the triangle points downstream, its point on the bar -->
	<path d="M 0.5 {top} L {bar} {cy} L 0.5 {bot} Z" fill="var(--ink-2, #c3c2b7)" stroke="var(--ink-2, #c3c2b7)" stroke-linejoin="round" />
	<line x1={bar} y1={top} x2={bar} y2={bot} stroke="var(--ink-2, #c3c2b7)" stroke-width="2.5" />
	{#if label}
		<!-- wider than the symbol: it may run past the box's sides -->
		<text x={width / 2} y={h - 2} text-anchor="middle" font-size="8" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
</style>
