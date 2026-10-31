<script lang="ts">
	// An immersion heater: an element coil that glows with power, convection
	// bubbles rising off it while it heats. Place it over a Tank, near the
	// floor of the vessel; size it to the vessel's width. The coil runs the
	// full width at any size, and the bubbles use the space above it.
	import { fitBox } from '../layout.js';
	import type { MimicLook } from '../mimic.js';

	let {
		powerPct = 0,
		width = 140,
		height,
		look = 'isa101'
	}: {
		powerPct?: number;
		width?: number;
		height?: number;
		look?: MimicLook;
	} = $props();

	const uid = `htr${Math.random().toString(36).slice(2, 8)}`;

	let box = $derived(fitBox(width, height, [140, 60]));
	let k = $derived(box.k);

	let heat = $derived(Math.max(0, Math.min(100, powerPct)) / 100);
	// Quantized for animation timing: a continuously varying duration would
	// restart the bubble animation on every data frame.
	let heatQ = $derived(Math.max(0.25, Math.round(heat * 4) / 4));

	// The coil zigzags across the bottom of the box.
	let coilTop = $derived(box.h - 26 * k);
	let coilBot = $derived(box.h - 8 * k);
	let coil = $derived.by(() => {
		const x0 = 6 * k;
		const x1 = box.w - 6 * k;
		const n = Math.max(2, Math.round((x1 - x0) / (20 * k)));
		let d = `M ${x0} ${coilBot}`;
		for (let i = 0; i < n; i++) {
			d += ` L ${x0 + ((x1 - x0) / n) * (i + 0.5)} ${i % 2 ? coilBot : coilTop}`;
		}
		return `${d} L ${x1} ${coilBot}`;
	});
	let bubbles = $derived(Math.max(2, Math.round(box.w / (28 * k))));

	let cold = $derived(look === 'realistic' ? '#8a6a4f' : 'var(--muted, #898781)');
	let hot = $derived(`hsl(${30 - heat * 22} 90% ${35 + heat * 25}%)`);
</script>

<svg
	viewBox="0 0 {box.w} {box.h}"
	width={box.w}
	height={box.h}
	role="img"
	aria-label={`Heater: ${(heat * 100).toFixed(0)}%`}
>
	<defs>
		<!-- the coil's bbox is short, so the default ±10% filter region clips
		     the blur into hard edges — give it explicit headroom -->
		<filter id="{uid}-glow" x="-20%" y="-150%" width="140%" height="400%">
			<feGaussianBlur stdDeviation={3 * k} />
		</filter>
		<clipPath id="{uid}-clip">
			<rect width={box.w} height={box.h} />
		</clipPath>
	</defs>

	{#if heat > 0.05}
		<g clip-path="url(#{uid}-clip)">
			{#each Array.from({ length: bubbles }, (_, i) => i) as i (i)}
				<circle
					class="bubble"
					style="animation-delay: {i * 0.7}s; animation-duration: {3.4 - heatQ * 1.6}s; --rise: {-coilTop}px"
					cx={((i + 0.5) / bubbles) * box.w}
					cy={coilTop}
					r={(2 + (i % 3)) * k}
					fill="rgba(255,255,255,0.5)"
					opacity={0.25 + heat * 0.5}
				/>
			{/each}
		</g>
	{/if}
	{#if heat > 0.02}
		<path class="glowpath" d={coil} fill="none" stroke="#ff8c42" stroke-width={7 * k} opacity={heat * 0.9} filter="url(#{uid}-glow)" />
	{/if}
	<path
		data-part="element"
		class="coil"
		d={coil}
		fill="none"
		stroke={heat > 0.02 ? hot : cold}
		stroke-width={Math.max(1.5, 4 * k)}
		stroke-linecap="round"
		stroke-linejoin="round"
	/>
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
	}
	.coil {
		transition: stroke 0.3s linear;
	}
	.glowpath {
		transition: opacity 0.3s linear;
	}
	.bubble {
		animation-name: rise;
		animation-timing-function: linear;
		animation-iteration-count: infinite;
		transition: opacity 0.3s linear;
	}
	@keyframes rise {
		from {
			transform: translateY(0);
			opacity: 0.6;
		}
		to {
			transform: translateY(var(--rise));
			opacity: 0;
		}
	}
</style>
