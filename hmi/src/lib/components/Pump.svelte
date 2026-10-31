<script lang="ts">
	// Centrifugal pump: impeller spins when running, spin rate follows speed.
	// A pump is a symbol, so it keeps its shape when resized: the volute
	// scales with the shorter side and the suction and discharge nozzles run
	// out to the box's left and top edges (`data-part="suction"` and
	// `"discharge"`, where the default ports sit).
	import { motion } from '../motion.svelte.js';
	import { pumpGeometry } from '../drawings.js';
	import type { MimicLook } from '../mimic.js';

	let {
		running = false,
		speedPct = 0,
		label = '',
		width = 120,
		height,
		look = 'isa101'
	}: { running?: boolean; speedPct?: number; label?: string; width?: number; height?: number; look?: MimicLook } = $props();

	const uid = `pmp${Math.random().toString(36).slice(2, 8)}`;

	// active = running fast enough to show as live (drives the color);
	// animate = actually spin/pulse (suppressed under reduced motion).
	let active = $derived(running && speedPct > 2);
	let animate = $derived(active && !motion.reduced);
	// 100% speed → 0.5s/rev; slower speed → proportionally slower. Speed is
	// quantized to 5% steps so feedback noise doesn't restart the animation.
	let speedQ = $derived(Math.max(5, Math.round(speedPct / 5) * 5));
	let period = $derived(animate ? 50 / speedQ : 0);

	let box = $derived(pumpGeometry(width, height));
	let k = $derived(box.k);
	let cx = $derived(box.cx);
	let cy = $derived(box.cy);
	let r = $derived(box.r);

	let real = $derived(look === 'realistic');
	let casing = $derived(real ? `url(#${uid}-steel)` : running ? 'var(--ink-2, #c3c2b7)' : 'var(--surface-2, #232321)');
	let edge = $derived(real ? '#2e3134' : 'var(--edge, #7a7973)');
	let blade = $derived(
		real ? (active ? 'var(--s1, #3987e5)' : '#6b6f74') : active ? 'var(--ink-2, #c3c2b7)' : 'var(--muted, #898781)'
	);
	let font = $derived(Math.max(8, 12 * k));
</script>

<svg viewBox="0 0 {box.w} {box.h}" width={box.w} height={box.h} role="img" aria-label={`Pump ${label}: ${running ? 'running' : 'stopped'}`}>
	{#if real}
		<defs>
			<radialGradient id="{uid}-steel" cx="0.35" cy="0.3" r="0.8">
				<stop offset="0" stop-color="#dfe2e5" />
				<stop offset="0.55" stop-color="#9a9ea3" />
				<stop offset="1" stop-color="#4a4d51" />
			</radialGradient>
			<linearGradient id="{uid}-pipe-h" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0" stop-color="#c9ccd0" />
				<stop offset="1" stop-color="#55585c" />
			</linearGradient>
			<linearGradient id="{uid}-pipe-v" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#c9ccd0" />
				<stop offset="1" stop-color="#55585c" />
			</linearGradient>
		</defs>
	{/if}

	<!-- suction / discharge nozzles, out to the box edges -->
	<rect
		data-part="suction"
		x={box.suction.x}
		y={box.suction.y}
		width={box.suction.w}
		height={box.suction.h}
		fill={real ? `url(#${uid}-pipe-h)` : 'var(--surface-2, #232321)'}
		stroke={edge}
		stroke-width={Math.max(1, k)}
	/>
	<rect
		data-part="discharge"
		x={box.discharge.x}
		y={box.discharge.y}
		width={box.discharge.w}
		height={box.discharge.h}
		fill={real ? `url(#${uid}-pipe-v)` : 'var(--surface-2, #232321)'}
		stroke={edge}
		stroke-width={Math.max(1, k)}
	/>

	<!-- volute -->
	<circle class="casing" {cx} {cy} {r} fill={casing} stroke={edge} stroke-width={Math.max(1.5, 3 * k)} />
	<circle {cx} {cy} r={25 * k} fill={real ? '#15181b' : 'var(--bg, #0d0d0d)'} stroke={edge} stroke-width={Math.max(1, k)} />

	<!-- impeller -->
	<g class:spin={animate} style="--period: {period}s" transform-origin="{cx} {cy}">
		{#each [0, 120, 240] as a (a)}
			<path
				d="M {cx} {cy} Q {cx + 8 * k} {cy - 12 * k} {cx} {cy - 22 * k}"
				fill="none"
				stroke={blade}
				stroke-width={Math.max(1.5, 4 * k)}
				stroke-linecap="round"
				transform="rotate({a} {cx} {cy})"
			/>
		{/each}
		<circle {cx} {cy} r={4 * k} fill={blade} />
	</g>

	<!-- status lamp: colour for a normal state is the realistic look's,
	     not ISA-101's (there the solid casing says "running") -->
	{#if real}
		<circle cx={cx + 39 * k} cy={cy - 34 * k} r={6 * k} fill={running ? 'var(--good, #0ca30c)' : '#3a3d40'} stroke="#2e3134">
			{#if running && !motion.reduced}<animate attributeName="opacity" values="1;0.6;1" dur="1.6s" repeatCount="indefinite" />{/if}
		</circle>
	{/if}

	{#if label}
		<text x={cx} y={box.h - 6 * k} text-anchor="middle" font-size={font} font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	/* `width` stays a real attribute, but the rendered box also shrinks under
	   a narrower parent instead of overflowing it. */
	svg {
		max-width: 100%;
		height: auto;
	}
	.casing {
		transition: fill 0.25s linear;
	}
	.spin {
		animation: rot var(--period) linear infinite;
	}
	/* counterclockwise: the backward-curved blades must trail the rotation */
	@keyframes rot {
		to {
			transform: rotate(-360deg);
		}
	}
</style>
