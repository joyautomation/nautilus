<script lang="ts">
	// A float switch — one component, two instances in lift-station.mimic.json
	// (LSHH-101 high-high, LSLL-101 low-low), distinguished by `label`/`tripped`
	// binding to a different tag each. Ball floats up on rising level; past
	// its pivot angle it's "tripped" (contact made). Follows the kit's idiom:
	// theme vars, a `width` prop, a live-bindable `tripped` prop.
	import { motion } from '@joyautomation/nautilus-hmi';

	let {
		tripped = false,
		label = '',
		width = 46
	}: { tripped?: boolean; label?: string; width?: number } = $props();

	// Untripped: float hangs down-and-out (low level clear of it). Tripped:
	// float has risen to swing the pivot arm up toward horizontal.
	let angle = $derived(tripped ? -55 : 20);
	let ballColor = $derived(tripped ? 'var(--serious, #ec835a)' : 'var(--muted, #8a887d)');
</script>

<svg
	viewBox="0 0 46 70"
	{width}
	role="img"
	aria-label={`Float switch ${label}: ${tripped ? 'tripped' : 'clear'}`}
>
	<!-- mount / pigtail, wall side -->
	<rect x="0" y="2" width="10" height="10" rx="2" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />
	<line x1="10" y1="7" x2="18" y2="7" stroke="var(--axis, #383835)" stroke-width="2" />

	<!-- pivot arm + float, rotating about the mount -->
	<g
		transform="translate(18 7) rotate({angle})"
		style={!motion.reduced ? 'transition: transform 0.4s ease' : ''}
	>
		<line x1="0" y1="0" x2="0" y2="40" stroke="var(--axis, #383835)" stroke-width="2.5" />
		<circle cx="0" cy="46" r="10" fill={ballColor} stroke="var(--axis, #383835)" stroke-width="2" />
	</g>

	<!-- tripped indicator lamp -->
	<circle cx="38" cy="8" r="5" fill={tripped ? 'var(--serious, #ec835a)' : 'var(--surface-2, #232321)'} stroke="var(--axis, #383835)" />

	{#if label}
		<text x="23" y="68" text-anchor="middle" font-size="10" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>
