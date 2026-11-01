<script lang="ts">
	// A float switch, drawn as the 3D kind is built (hmi/models/build.py,
	// build_float): a ball on a tether hung from its cable clamp. Above the
	// level it hangs straight down; below it, the level has floated it up
	// and out on its tether, toward the middle of the well. `trip` says
	// which of those is the alarm: a high switch (LSHH, the default) trips
	// when the level floats it up, a low switch (LSLL) when the level falls
	// away and it drops. One component, two instances in
	// lift-station.mimic.json, each with its own `label`, `trip` and
	// `tripped` tag. Both ball positions sit inside the box, so neither is
	// clipped. The clamp is `data-part="clamp"`.
	import { fitBox, motion, type MimicLook } from '@joyautomation/nautilus-hmi';

	let {
		tripped = false,
		trip = 'high',
		label = '',
		width = 52,
		height,
		look = 'isa101'
	}: { tripped?: boolean; trip?: 'high' | 'low'; label?: string; width?: number; height?: number; look?: MimicLook } = $props();

	const uid = `fs${Math.random().toString(36).slice(2, 8)}`;

	// Drawn at 52 x 64 and scaled to fit; extra room goes to the right and
	// below (the clamp stays where the instance is placed).
	let box = $derived(fitBox(width, height, [52, 64]));
	let k = $derived(box.k);

	// Floated up: a high switch when tripped, a low switch when clear.
	let up = $derived(trip === 'low' ? !tripped : tripped);
	// The tether is 33 long. Down = 0 deg; floated up swings it 61 deg
	// toward +x, which puts the ball 29 out and 16 down, the 3D kind's
	// FloatUp.
	let angle = $derived(up ? -61 : 0);

	let real = $derived(look === 'realistic');
	// Tripped is a switch state worth noticing (high-high, low-low): it
	// takes the alarm hue in both looks.
	let ball = $derived(tripped ? 'var(--serious, #ec835a)' : real ? `url(#${uid}-ball)` : 'var(--surface-2, #232321)');
	let edge = $derived(real ? '#2e3134' : 'var(--ink-2, #c3c2b7)');
</script>

<svg
	viewBox="0 0 {box.w} {box.h}"
	width={box.w}
	height={box.h}
	role="img"
	aria-label={`${trip === 'low' ? 'Low' : 'High'} float switch ${label}: ${tripped ? 'tripped' : 'clear'}`}
>
	{#if real}
		<defs>
			<radialGradient id="{uid}-ball" cx="0.35" cy="0.3" r="0.75">
				<stop offset="0" stop-color="#f4f3ee" />
				<stop offset="1" stop-color="#9b9a93" />
			</radialGradient>
		</defs>
	{/if}

	<g transform="scale({k})">
		<!-- cable down to the clamp -->
		<line x1="10" y1="0" x2="10" y2="5" stroke={real ? '#111' : 'var(--muted, #898781)'} stroke-width="2" />

		<!-- tether + ball, swinging about the clamp -->
		<g transform="translate(10 6)">
			<g class:swing={!motion.reduced} style="transform: rotate({angle}deg)">
				<line x1="0" y1="0" x2="0" y2="24" stroke={real ? '#111' : 'var(--muted, #898781)'} stroke-width="1.5" />
				<circle class="ball" cx="0" cy="33" r="9" fill={ball} stroke={edge} stroke-width="1.5" />
			</g>
		</g>

		<rect data-part="clamp" x="7" y="4" width="6" height="4" rx="1" fill={real ? '#55585c' : 'var(--surface-2, #232321)'} stroke={edge} stroke-width="1" />

		{#if label}
			<text x="26" y="61" text-anchor="middle" font-size="10" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
		{/if}
	</g>
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
	}
	.swing {
		transition: transform 0.4s ease;
	}
	.ball {
		transition: fill 0.25s linear;
	}
</style>
