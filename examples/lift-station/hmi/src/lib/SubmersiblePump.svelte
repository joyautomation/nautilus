<script lang="ts">
	// A submersible sewage pump, drawn as the 3D kind is built
	// (hmi/models/build.py, build_pump): stood on the well floor on three
	// feet, a squat volute, a long motor can, a cap with the running lamp and
	// the power cable, and the discharge leaving the volute low down, turning
	// up through an elbow to a flanged stub beside the motor. The elbow and
	// the flange are parts (`data-part`) a port can anchor to; the shared
	// discharge port sits on top of the elbow.
	//
	// Laid out, not stretched: made taller, the motor can grows and the feet,
	// volute, cap and label keep their size; the `look` prop picks ISA-101
	// (flat, solid when running) or realistic (shaded steel, a lit lamp).
	import { fitBox, motion, tracks, type MimicLook } from '@joyautomation/nautilus-hmi';

	let {
		running = false,
		speedHz = 0,
		label = '',
		width = 100,
		height,
		look = 'isa101'
	}: { running?: boolean; speedHz?: number; label?: string; width?: number; height?: number; look?: MimicLook } = $props();

	const uid = `sp${Math.random().toString(36).slice(2, 8)}`;

	// active = actually pumping (drives the impeller); a running pump at
	// ~0 Hz (starting/stopping) doesn't turn yet.
	let active = $derived(running && speedHz > 2);
	let animate = $derived(active && !motion.reduced);
	// 60 Hz -> ~0.5 s per vane pass; slower speed passes proportionally
	// slower. Quantized so feedback noise doesn't restart the animation.
	let speedQ = $derived(Math.max(5, Math.round(speedHz / 5) * 5));
	let period = $derived(animate ? 30 / speedQ : 0);

	let box = $derived(fitBox(width, height, [100, 170]));
	let k = $derived(box.k);
	// top to bottom: cable, cap, motor (takes the slack), volute, feet, label
	let rows = $derived(tracks(box.h, [18 * k, 12 * k, '1fr', 30 * k, 14 * k, label ? 18 * k : 3 * k]));
	let cable = $derived(rows[0]);
	let cap = $derived(rows[1]);
	let motor = $derived(rows[2]);
	let volute = $derived(rows[3]);
	let feet = $derived(rows[4]);

	// Horizontal: the body's centre line, with the discharge riser 50 units
	// to its right — the whole drawing (volute's left edge to the flange's
	// right edge) centred in the box.
	let bx = $derived(box.w / 2 - 15.5 * k);
	let rx = $derived(bx + 50 * k);

	let real = $derived(look === 'realistic');
	let edge = $derived(real ? '#2e3134' : 'var(--edge, #7a7973)');
	let sw = $derived(Math.max(1, 1.5 * k));
	// ISA-101: running is said by a solid body, not a hue.
	let bodyFill = $derived(real ? `url(#${uid}-steel)` : running ? 'var(--ink-2, #c3c2b7)' : 'var(--surface-2, #232321)');
	let darkFill = $derived(real ? `url(#${uid}-dark)` : 'var(--surface-2, #232321)');
	let pipeFill = $derived(real ? `url(#${uid}-steel)` : 'var(--surface-2, #232321)');
	let font = $derived(Math.max(8, 12 * k));

	// discharge: out of the volute low down, the elbow, the stub up to the flange
	let dy = $derived(volute.end - 9 * k);
	let flangeY = $derived(volute.start - 4 * k);
</script>

<svg
	viewBox="0 0 {box.w} {box.h}"
	width={box.w}
	height={box.h}
	role="img"
	aria-label={`Submersible pump ${label}: ${running ? `running${active ? ` at ${speedHz.toFixed(0)} Hz` : ''}` : 'stopped'}`}
>
	<defs>
		{#if real}
			<!-- cylinders lit from the upper left -->
			<linearGradient id="{uid}-steel" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#55595e" />
				<stop offset="0.3" stop-color="#d2d5d9" />
				<stop offset="0.6" stop-color="#9a9ea3" />
				<stop offset="1" stop-color="#44474b" />
			</linearGradient>
			<linearGradient id="{uid}-dark" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#2c2f32" />
				<stop offset="0.35" stop-color="#6d7176" />
				<stop offset="1" stop-color="#26282b" />
			</linearGradient>
			<radialGradient id="{uid}-lamp">
				<stop offset="0" stop-color="#d8ffd8" />
				<stop offset="0.5" stop-color="var(--good, #0ca30c)" />
				<stop offset="1" stop-color="var(--good, #0ca30c)" stop-opacity="0" />
			</radialGradient>
		{/if}
		<clipPath id="{uid}-eye">
			<rect x={bx - 13 * k} y={feet.start} width={26 * k} height={5 * k} />
		</clipPath>
	</defs>

	<!-- power cable, up out of the cap -->
	<line x1={bx + 4 * k} y1={cable.start} x2={bx + 4 * k} y2={cap.start + 4 * k} stroke={real ? '#111' : 'var(--muted, #898781)'} stroke-width={Math.max(1, 2 * k)} />

	<!-- feet: three, one in front and two to the sides -->
	{#each [-19, 0, 19] as fx (fx)}
		<rect x={bx + (fx - 3) * k} y={feet.start} width={6 * k} height={feet.size} fill={darkFill} stroke={edge} stroke-width={sw} />
	{/each}

	<!-- impeller in the suction eye under the volute: vanes passing -->
	<g clip-path="url(#{uid}-eye)">
		<rect x={bx - 13 * k} y={feet.start} width={26 * k} height={5 * k} fill={real ? '#1a1c1e' : 'var(--bg, #0d0d0d)'} />
		<g class:pass={animate} style="--period: {period}s; --pitch: {9 * k}px">
			{#each [-4, -3, -2, -1, 0, 1, 2] as i (i)}
				<rect
					x={bx + (i * 9 - 1) * k}
					y={feet.start}
					width={2.5 * k}
					height={5 * k}
					fill={active ? (real ? 'var(--s1, #3987e5)' : 'var(--ink-2, #c3c2b7)') : 'var(--muted, #898781)'}
				/>
			{/each}
		</g>
	</g>

	<!-- discharge: a horizontal run out of the volute, the elbow, the stub -->
	<rect x={bx + 26 * k} y={dy - 6 * k} width={rx - bx - 26 * k} height={12 * k} fill={pipeFill} stroke={edge} stroke-width={sw} />
	<rect x={rx - 7 * k} y={flangeY + 3 * k} width={14 * k} height={dy - flangeY - 3 * k} fill={pipeFill} stroke={edge} stroke-width={sw} />
	<circle data-part="elbow" cx={rx} cy={dy} r={7.5 * k} fill={pipeFill} stroke={edge} stroke-width={sw} />
	<rect data-part="flange" x={rx - 11 * k} y={flangeY} width={22 * k} height={3 * k} rx={k} fill={darkFill} stroke={edge} stroke-width={sw} />

	<!-- volute -->
	<rect
		data-part="volute"
		class="body"
		x={bx - 30 * k}
		y={volute.start}
		width={60 * k}
		height={volute.size}
		rx={6 * k}
		fill={bodyFill}
		stroke={edge}
		stroke-width={Math.max(1, 2 * k)}
	/>

	<!-- strap from the motor to where the discharge pipe passes it -->
	<rect x={bx + 21 * k} y={motor.start + motor.size * 0.35} width={rx - bx - 27 * k} height={5 * k} fill={darkFill} stroke={edge} stroke-width={sw} />

	<!-- motor can -->
	<rect
		data-part="motor"
		class="body"
		x={bx - 21 * k}
		y={motor.start}
		width={42 * k}
		height={motor.size}
		rx={4 * k}
		fill={bodyFill}
		stroke={edge}
		stroke-width={Math.max(1, 2 * k)}
	/>
	{#if real}
		<!-- cooling ribs -->
		{#each [0.25, 0.5, 0.75] as f (f)}
			<line x1={bx - 21 * k + sw} x2={bx + 21 * k - sw} y1={motor.start + motor.size * f} y2={motor.start + motor.size * f} stroke="rgba(0,0,0,0.25)" stroke-width={Math.max(1, k)} />
		{/each}
		{#if running}
			<!-- the 3D kind tints the motor and volute while running -->
			<rect x={bx - 21 * k} y={motor.start} width={42 * k} height={motor.size} rx={4 * k} fill="var(--good, #0ca30c)" opacity="0.22" />
			<rect x={bx - 30 * k} y={volute.start} width={60 * k} height={volute.size} rx={6 * k} fill="var(--good, #0ca30c)" opacity="0.22" />
		{/if}
	{/if}

	<!-- cap: a cone from the motor up to the cable gland -->
	<path
		d="M {bx - 21 * k} {cap.end} L {bx - 9 * k} {cap.start} L {bx + 9 * k} {cap.start} L {bx + 21 * k} {cap.end} Z"
		fill={darkFill}
		stroke={edge}
		stroke-width={sw}
		stroke-linejoin="round"
	/>

	<!-- running lamp on the cap -->
	{#if real}
		{#if running}
			<circle cx={bx - 6 * k} cy={cap.start + 4 * k} r={8 * k} fill="url(#{uid}-lamp)">
				{#if !motion.reduced}<animate attributeName="opacity" values="1;0.55;1" dur="1.4s" repeatCount="indefinite" />{/if}
			</circle>
		{/if}
		<circle cx={bx - 6 * k} cy={cap.start + 4 * k} r={3.5 * k} fill={running ? '#b8ffb8' : '#3a3d40'} stroke="#2e3134" stroke-width={Math.max(0.5, 0.75 * k)} />
	{/if}

	{#if label}
		<text x={bx} y={box.h - 4 * k} text-anchor="middle" font-size={font} font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
	}
	.body {
		transition: fill 0.25s linear;
	}
	/* Vanes pass across the eye one pitch per period and repeat: the
	   elevation view of an impeller turning. */
	.pass {
		animation: pass var(--period) linear infinite;
	}
	@keyframes pass {
		to {
			transform: translateX(var(--pitch));
		}
	}
</style>
