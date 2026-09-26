<script lang="ts">
	// A submersible sewage pump: sits IN the wet well (no suction pipe to
	// draw — the mimic places it inside/below WW-101's outline), one
	// discharge port on top where the force main leaves the well. Follows
	// the kit's idiom (Tank/Pump/Valve): theme vars, a `width` prop, a
	// couple of live-bindable props, reduced-motion respected.
	import { motion } from '@joyautomation/nautilus-hmi';

	let {
		running = false,
		speedHz = 0,
		label = '',
		width = 90
	}: { running?: boolean; speedHz?: number; label?: string; width?: number } = $props();

	// active = actually pumping (drives color + motion); a running pump at
	// ~0 Hz (starting/stopping) doesn't spin the impeller yet.
	let active = $derived(running && speedHz > 2);
	let animate = $derived(active && !motion.reduced);
	// 60 Hz -> ~0.6s/rev; slower speed spins proportionally slower.
	let speedQ = $derived(Math.max(5, Math.round(speedHz / 5) * 5));
	let period = $derived(animate ? 30 / speedQ : 0);
	let bodyColor = $derived(active ? 'var(--s1, #3987e5)' : 'var(--surface-2, #232321)');
</script>

<svg
	viewBox="0 0 90 130"
	{width}
	role="img"
	aria-label={`Submersible pump ${label}: ${running ? `running${active ? ` at ${speedHz.toFixed(0)} Hz` : ''}` : 'stopped'}`}
>
	<!-- discharge stub, top-center -->
	<rect x="39" y="0" width="12" height="16" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />

	<!-- volute / motor casing, cylindrical -->
	<rect x="14" y="16" width="62" height="82" rx="14" fill="var(--surface-2, #232321)" stroke={bodyColor} stroke-width="3" />

	<!-- impeller housing window -->
	<circle cx="45" cy="52" r="20" fill="var(--bg, #0d0d0d)" stroke="var(--axis, #383835)" stroke-width="2" />
	<g style={animate ? `animation: spin ${period}s linear infinite` : ''} transform-origin="45 52">
		<line x1="45" y1="36" x2="45" y2="68" stroke={bodyColor} stroke-width="3" stroke-linecap="round" />
		<line x1="31" y1="52" x2="59" y2="52" stroke={bodyColor} stroke-width="3" stroke-linecap="round" />
		<line x1="34.7" y1="41.7" x2="55.3" y2="62.3" stroke={bodyColor} stroke-width="3" stroke-linecap="round" />
		<line x1="55.3" y1="41.7" x2="34.7" y2="62.3" stroke={bodyColor} stroke-width="3" stroke-linecap="round" />
	</g>

	<!-- motor base / legs, tapered foot on the wet-well floor -->
	<path d="M 20 98 L 70 98 L 60 116 L 30 116 Z" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />

	<!-- running lamp -->
	<circle cx="70" cy="24" r="6" fill={running ? 'var(--good, #0ca30c)' : 'var(--surface-2, #232321)'} stroke="var(--axis, #383835)">
		{#if running && !motion.reduced}<animate attributeName="opacity" values="1;0.55;1" dur="1.4s" repeatCount="indefinite" />{/if}
	</circle>

	{#if label}
		<text x="45" y="128" text-anchor="middle" font-size="12" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
