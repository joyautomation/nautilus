<script lang="ts">
	// Swing check valve: purely mechanical, so no actuator, no position and
	// no signal. Drawn as the P&ID check valve: a solid triangle pointing the
	// way flow may go, its point against a bar that stops it coming back,
	// the pipe meeting it on both sides. `flow` is the way it passes:
	// `right` (the default: in on the left, out on the right, the label
	// under it) or `up` (in at the bottom, out at the top, in a riser, the
	// label beside it). The width sets the size: for `right` it is the
	// length along the flow, for `up` the box across it (the label room on
	// both sides included); the triangle is four fifths as wide across as
	// it is long. Its ports in
	// CheckValve.component.json are for `right`; an `up` instance carries
	// its own (in at x 0.5, y 1; out at x 0.5, y 0).
	// App-supplied via <Mimic registry>, not part of the kit.
	let { label = '', width = 26, flow = 'right' }: { label?: string; width?: number; flow?: 'right' | 'up' } = $props();

	const pad = 12;
	const up = $derived(flow === 'up');
	// along: the valve's length in the flow's direction
	const body = $derived(up ? Math.max(4, width - 2 * pad) : width * 0.8);
	const along = $derived(up ? body / 0.8 : width);
	const w = $derived(up ? width : along);
	const h = $derived(up ? along : body + 2 * pad);
	const bar = $derived(along - 1.25);
</script>

<svg viewBox="0 0 {w} {h}" width={w} height={h} role="img" aria-label={`Check valve ${label}, flow ${up ? 'upward' : 'left to right'} only`}>
	{#if up}
		<rect x={pad} y="0" width={body} height={along} fill="var(--bg, #0d0d0d)" />
		<!-- the triangle points up, its point on the bar -->
		<path d="M {pad} {along - 0.5} L {w / 2} {1.25} L {pad + body} {along - 0.5} Z" fill="var(--ink-2, #c3c2b7)" stroke="var(--ink-2, #c3c2b7)" stroke-linejoin="round" />
		<line x1={pad} y1="1.25" x2={pad + body} y2="1.25" stroke="var(--ink-2, #c3c2b7)" stroke-width="2.5" />
		{#if label}
			<text x={w - pad + 5} y={h / 2 + 4} font-size="11" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
		{/if}
	{:else}
		<!-- over the pipes' rounded ends, which reach a little inside the box -->
		<rect x="0" y={pad} {width} height={body} fill="var(--bg, #0d0d0d)" />
		<!-- the triangle points downstream, its point on the bar -->
		<path d="M 0.5 {pad} L {bar} {h / 2} L 0.5 {pad + body} Z" fill="var(--ink-2, #c3c2b7)" stroke="var(--ink-2, #c3c2b7)" stroke-linejoin="round" />
		<line x1={bar} y1={pad} x2={bar} y2={pad + body} stroke="var(--ink-2, #c3c2b7)" stroke-width="2.5" />
		{#if label}
			<!-- wider than the symbol: it may run past the box's sides -->
			<text x={width / 2} y={h - 2} text-anchor="middle" font-size="11" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
		{/if}
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
</style>
