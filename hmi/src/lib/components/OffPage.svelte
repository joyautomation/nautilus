<script lang="ts">
	// A P&ID's off-page connector: the arrow a line runs into where the
	// drawing stops showing it, named for where it goes or comes from
	// ("TO WASTE DRAIN", "FROM P-501"). A screen uses it where a line would
	// cross a stack of headers to reach its own: the drop ends at a
	// connector named for its header, and the header is drawn once, beside
	// what feeds it. `dir` is the way the arrow points: the way the line
	// flows.
	import type { MimicLook } from '../mimic.js';

	let {
		label = '',
		dir = 'right',
		width = 150,
		height,
		look = 'isa101'
	}: {
		/** Where the line goes or comes from. */
		label?: string;
		/** The way the arrow points: the way the line flows. */
		dir?: 'right' | 'left' | 'up' | 'down';
		width?: number;
		height?: number;
		look?: MimicLook;
	} = $props();

	const flat = $derived(dir === 'right' || dir === 'left');
	const w = $derived(Math.max(40, width));
	const h = $derived(Math.max(20, height ?? (flat ? 32 : 44)));
	const s = 1; // half the 2 px outline
	// how far the point stands out from the box
	const p = $derived(flat ? h / 2 : Math.min(12, h / 3));
	const d = $derived.by(() => {
		switch (dir) {
			case 'left':
				return `M ${w - s} ${s} L ${p} ${s} L ${s} ${h / 2} L ${p} ${h - s} L ${w - s} ${h - s} Z`;
			case 'down':
				return `M ${s} ${s} L ${w - s} ${s} L ${w - s} ${h - p} L ${w / 2} ${h - s} L ${s} ${h - p} Z`;
			case 'up':
				return `M ${s} ${h - s} L ${w - s} ${h - s} L ${w - s} ${p} L ${w / 2} ${s} L ${s} ${p} Z`;
			default:
				return `M ${s} ${s} L ${w - p} ${s} L ${w - s} ${h / 2} L ${w - p} ${h - s} L ${s} ${h - s} Z`;
		}
	});
	// the text sits in the box, clear of the point
	const tx = $derived(dir === 'right' ? (w - p) / 2 : dir === 'left' ? (w + p) / 2 : w / 2);
	const ty = $derived(dir === 'down' ? (h - p) / 2 : dir === 'up' ? (h + p) / 2 : h / 2);
	const real = $derived(look === 'realistic');
</script>

<svg viewBox="0 0 {w} {h}" width={w} height={h} role="img" aria-label={`Off-page connector: ${label}`}>
	<!-- the connector's extent, for port anchoring -->
	<rect data-part="body" x="0" y="0" width={w} height={h} fill="none" />
	<path
		{d}
		fill={real ? 'var(--surface-2, #232321)' : 'var(--surface, #161615)'}
		stroke="var(--pipe-wall, var(--edge, #7a7973))"
		stroke-width={s * 2}
		stroke-linejoin="round"
	/>
	<text x={tx} y={ty} text-anchor="middle" dominant-baseline="central" font-size="12" font-weight="600" fill="var(--ink-2, #c3c2b7)">
		{label}
	</text>
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
	text {
		letter-spacing: 0.02em;
	}
</style>
