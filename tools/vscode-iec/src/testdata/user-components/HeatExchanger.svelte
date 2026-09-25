<script lang="ts">
	// A custom component living OUTSIDE the kit — the worked example for
	// registry + HeatExchanger.component.json (see the sidecar file next to this
	// one). Shell-and-tube heat exchanger:
	// tube-side process fluid runs left-to-right through the shell, a
	// separate shell-side (cooling/heating) fluid enters/exits top and
	// bottom. Follows the kit's idiom (Tank/Pump/Valve): theme vars, a
	// `width` prop that drives the box, `motion` for reduced-motion users.
	import { motion } from '@joyautomation/nautilus-hmi';

	let {
		active = false,
		label = '',
		width = 160
	}: { active?: boolean; label?: string; width?: number } = $props();

	let animate = $derived(active && !motion.reduced);
	let tubeColor = $derived(active ? 'var(--s1, #3987e5)' : 'var(--muted, #898781)');
	const uid = `hx${Math.random().toString(36).slice(2, 8)}`;
</script>

<svg
	viewBox="0 0 160 100"
	{width}
	role="img"
	aria-label={`Heat exchanger ${label}: ${active ? 'in service' : 'idle'}`}
>
	<defs>
		<!-- tube bundle is clipped to the shell cavity so the lines break at
		     the rounded ends instead of poking past the wall -->
		<clipPath id="{uid}-cavity">
			<rect x="24" y="24" width="112" height="52" rx="26" />
		</clipPath>
	</defs>

	<!-- shell-side nozzles: top/bottom, centered -->
	<rect x="74" y="0" width="12" height="20" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />
	<rect x="74" y="80" width="12" height="20" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />

	<!-- tube-side nozzles: left/right, centered -->
	<rect x="0" y="44" width="20" height="12" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />
	<rect x="140" y="44" width="20" height="12" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />

	<!-- shell body: a capsule (rx = height/2 gives semicircular heads) -->
	<rect x="20" y="20" width="120" height="60" rx="30" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />
	<rect x="24" y="24" width="112" height="52" rx="26" fill="var(--bg, #0d0d0d)" />

	<g clip-path="url(#{uid}-cavity)">
		<!-- tube bundle -->
		{#each [36, 50, 64] as y}
			<line
				class="tube"
				class:flow={animate}
				x1="24" y1={y} x2="136" y2={y}
				stroke={tubeColor}
				stroke-width="3"
				stroke-linecap="round"
				stroke-dasharray={animate ? '7 6' : 'none'}
			/>
		{/each}
	</g>

	<!-- in-service lamp -->
	<circle cx="146" cy="12" r="6" fill={active ? 'var(--good, #0ca30c)' : 'var(--surface-2, #232321)'} stroke="var(--axis, #383835)">
		{#if active && !motion.reduced}<animate attributeName="opacity" values="1;0.6;1" dur="1.6s" repeatCount="indefinite" />{/if}
	</circle>

	{#if label}
		<text x="80" y="96" text-anchor="middle" font-size="12" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	.tube.flow {
		animation: march 0.7s linear infinite;
	}
	@keyframes march {
		to {
			stroke-dashoffset: -13;
		}
	}
</style>
