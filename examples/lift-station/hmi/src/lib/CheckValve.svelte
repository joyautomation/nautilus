<script lang="ts">
	// Swing check valve: purely mechanical, so no actuator, no position and
	// no signal. The bow-tie body with its downstream half filled (the P&ID
	// check valve symbol) and a flow arrow above for the one direction it
	// passes. It draws in the same box as the kit's Valve at the same width
	// (90 x 80 units, the line 55 % of the way down), so swapping one for the
	// other leaves the pipes where they were. App-supplied via
	// <Mimic registry>, not part of the kit.
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
	const x1 = $derived(width - s / 2);
	const ay = $derived(14 * k);
</script>

<svg viewBox="0 0 {width} {h}" {width} height={h} role="img" aria-label={`Check valve ${label}, flow left to right only`}>
	<!-- upstream half open, downstream half filled -->
	<path d="M {x0} {top} L {cx} {cy} L {x0} {bot} Z" fill="var(--bg, #0d0d0d)" stroke="var(--ink-2, #c3c2b7)" stroke-width={s} stroke-linejoin="round" />
	<path d="M {x1} {top} L {cx} {cy} L {x1} {bot} Z" fill="var(--ink-2, #c3c2b7)" stroke="var(--ink-2, #c3c2b7)" stroke-width={s} stroke-linejoin="round" />
	<!-- the direction it lets flow through -->
	<line x1={cx - 16 * k} y1={ay} x2={cx + 10 * k} y2={ay} stroke="var(--muted, #8a887d)" stroke-width={s} />
	<path d="M {cx + 16 * k} {ay} L {cx + 8 * k} {ay - 5 * k} L {cx + 8 * k} {ay + 5 * k} Z" fill="var(--muted, #8a887d)" />
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
