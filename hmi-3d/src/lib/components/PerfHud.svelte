<script lang="ts">
	// The measurement strip: fps, p95 tag-change -> pixel, sample count and
	// active alarms. Opt-in (`perf` on SceneView); only meaningful in a
	// visible window — see perf.svelte.ts.
	import { ConnectionBadge } from '@joyautomation/nautilus-hmi';
	import type { PerfSampler } from '../perf.svelte.js';

	let {
		sampler,
		connected = false,
		active = 0,
		label = 'controller'
	}: { sampler: PerfSampler; connected?: boolean; active?: number; label?: string } = $props();
</script>

<div class="hud">
	<ConnectionBadge state={connected ? 'connected' : 'offline'} {label} size="sm" />
	<span>{sampler.fps} fps</span>
	<span title="controller frame timestamp to the animation frame after the one that drew it; needs a shared clock"
		>ts→pixel p95 {sampler.p95.toFixed(0)} ms <small>(n={sampler.samples})</small></span
	>
	<span>{active} active alarm{active === 1 ? '' : 's'}</span>
</div>

<style>
	.hud {
		display: flex;
		gap: 12px;
		align-items: center;
		padding: 6px 10px;
		border-radius: 6px;
		font: 13px system-ui, sans-serif;
		background: color-mix(in srgb, var(--surface, #161615) 85%, transparent);
		border: 1px solid var(--axis, #383835);
	}
	small {
		color: var(--muted, #898781);
	}
</style>
