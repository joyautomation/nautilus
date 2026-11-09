<script lang="ts">
	// A vessel whose liquid height follows level. Laid out, not stretched:
	// give it a `height` and the vessel grows while the scale and the label
	// keep their size. The vessel is `data-part="vessel"`, so a port anchored
	// to it stays on the wall however the tank is sized. Heating is its own
	// component (Heater), placed over the tank.
	//
	// `shape` says what kind of tank it is: a closed `vessel` (the default),
	// an `open` basin with no roof, or a below-grade `pit` (a wet well or a
	// sump: square concrete walls, a cover slab with an access hatch at
	// grade, and hatched ground around it).
	import { tankGeometry, type TankShape } from '../drawings.js';
	import type { MimicLook } from '../mimic.js';

	let {
		levelPct = 50,
		label = '',
		width = 220,
		height,
		look = 'isa101',
		scale = true,
		shape = 'vessel'
	}: {
		levelPct?: number;
		label?: string;
		width?: number;
		height?: number;
		look?: MimicLook;
		/** Draw the 0–100 level graduations beside the vessel. */
		scale?: boolean;
		/** `vessel` (closed), `open` (no roof) or `pit` (below grade). */
		shape?: TankShape;
	} = $props();

	const uid = `tank${Math.random().toString(36).slice(2, 8)}`;

	let box = $derived(tankGeometry(width, height, { label, scale, shape }));
	let pit = $derived(shape === 'pit');
	let open = $derived(shape === 'open');
	let g = $derived(box.ground);
	let k = $derived(box.k);

	// vessel (outer shell) and its interior
	let vx = $derived(box.vessel.x);
	let vy = $derived(box.vessel.y);
	let vw = $derived(box.vessel.w);
	let vh = $derived(box.vessel.h);
	// A pit's walls are concrete: thicker, and square.
	let wall = $derived(pit ? Math.max(3, 9 * k) : Math.max(2, 6 * k));
	let rx = $derived(pit ? 0 : Math.min(14 * k, vw / 3, vh / 3));
	let ix = $derived(vx + wall);
	// An open basin has no roof: its inside runs to the top of the walls.
	let iy = $derived(open ? vy : vy + wall);
	let iw = $derived(Math.max(0, vw - 2 * wall));
	let ih = $derived(Math.max(0, vh - (open ? 1 : 2) * wall));
	let irx = $derived(Math.max(0, rx - wall / 2));

	let clamped = $derived(Math.max(0, Math.min(100, levelPct)));
	let liquidH = $derived((clamped / 100) * ih);
	let liquidY = $derived(iy + ih - liquidH);

	// Graduations thin out on a short vessel so the numbers never collide.
	let marks = $derived(ih < 90 ? [0, 50, 100] : [0, 25, 50, 75, 100]);
	let stroke = $derived(Math.max(1, 2 * k));
	let font = $derived(box.font);

	let real = $derived(look === 'realistic');
	let name = $derived(pit ? 'Pit' : open ? 'Basin' : 'Tank');
	let shellFill = $derived(real ? (pit ? '#8f8c84' : `url(#${uid}-steel)`) : 'var(--surface-2, #232321)');
	let edge = $derived(real ? '#2e3134' : 'var(--edge, #7a7973)');
	// The access hatch in a pit's cover slab, a third of the way across.
	let hatchX = $derived(vx + vw * 0.2);
	let hatchW = $derived(vw * 0.28);
</script>

<svg
	viewBox="0 0 {box.w} {box.h}"
	width={box.w}
	height={box.h}
	role="img"
	aria-label={`${name} ${label}: ${clamped.toFixed(0)}%`}
>
	<defs>
		{#if pit}
			<!-- ground: the civil drawing's diagonal hatch -->
			<pattern id="{uid}-earth" width={6 * k} height={6 * k} patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
				<line x1="0" y1="0" x2="0" y2={6 * k} stroke="var(--muted, #898781)" stroke-width={Math.max(0.75, k)} opacity="0.55" />
			</pattern>
		{/if}
		<clipPath id="{uid}-clip">
			<rect x={ix} y={iy} width={iw} height={ih} rx={irx} />
		</clipPath>
		{#if real}
			<!-- a cylinder lit from the upper left: the highlight a third of
			     the way across, the far side falling off into shadow -->
			<linearGradient id="{uid}-steel" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#4f5357" />
				<stop offset="0.22" stop-color="#cfd3d7" />
				<stop offset="0.5" stop-color="#9da1a6" />
				<stop offset="1" stop-color="#43464a" />
			</linearGradient>
			<linearGradient id="{uid}-bore" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#0b0d0f" />
				<stop offset="0.5" stop-color="#1c2024" />
				<stop offset="1" stop-color="#0b0d0f" />
			</linearGradient>
			<linearGradient id="{uid}-liq" x1="0" y1="0" x2="1" y2="0">
				<stop offset="0" stop-color="#1f4466" />
				<stop offset="0.3" stop-color="#5b93c7" />
				<stop offset="1" stop-color="#1a3a58" />
			</linearGradient>
		{/if}
	</defs>

	{#if pit}
		<!-- the ground around the pit, and the grade line across its top -->
		<path
			d="M {vx - g} {vy} H {vx + vw + g} V {vy + vh + g} H {vx - g} Z M {vx} {vy} V {vy + vh} H {vx + vw} V {vy} Z"
			fill="url(#{uid}-earth)"
			fill-rule="evenodd"
		/>
		<line x1={vx - g - 6 * k} x2={vx + vw + g} y1={vy} y2={vy} stroke={edge} stroke-width={Math.max(1.5, 3 * k)} />
	{/if}

	<!-- shell -->
	{#if open}
		<!-- walls and floor, no roof -->
		<path
			data-part="vessel"
			d="M {vx} {vy} V {vy + vh - rx} Q {vx} {vy + vh} {vx + rx} {vy + vh} H {vx + vw - rx} Q {vx + vw} {vy + vh} {vx + vw} {vy + vh - rx} V {vy}"
			fill={shellFill}
			stroke={edge}
			stroke-width={stroke}
		/>
	{:else}
		<rect data-part="vessel" x={vx} y={vy} width={vw} height={vh} {rx} fill={shellFill} stroke={edge} stroke-width={stroke} />
	{/if}
	<rect x={ix} y={iy} width={iw} height={ih} rx={irx} fill={real ? `url(#${uid}-bore)` : 'var(--bg, #0d0d0d)'} />

	<g clip-path="url(#{uid}-clip)">
		<rect
			class="geo"
			x={ix}
			y={liquidY}
			width={iw}
			height={liquidH}
			fill={real ? `url(#${uid}-liq)` : 'var(--fluid, #35618a)'}
		/>
		<!-- surface line -->
		{#if clamped > 1 && clamped < 99}
			<rect class="geo" x={ix} y={liquidY} width={iw} height={Math.max(1, 2 * k)} fill={real ? 'rgba(220,235,250,0.55)' : 'rgba(255,255,255,0.25)'} />
		{/if}
	</g>

	{#if pit}
		<!-- the access hatch in the cover slab -->
		<rect x={hatchX} y={vy + stroke} width={hatchW} height={Math.max(1, wall - 2 * stroke)} fill="var(--bg, #0d0d0d)" stroke={edge} stroke-width={Math.max(1, k)} />
	{/if}

	{#if real && !pit}
		<!-- weld seams between the shell's courses -->
		{#each [1 / 3, 2 / 3] as f (f)}
			<line x1={vx + stroke} x2={vx + vw - stroke} y1={vy + vh * f} y2={vy + vh * f} stroke="rgba(0,0,0,0.28)" stroke-width={Math.max(1, k)} />
		{/each}
	{/if}

	<!-- level graduations -->
	{#if scale}
		{#each marks as mark (mark)}
			{@const gy = iy + ih - (mark / 100) * ih}
			<line x1={vx + vw + g + 6 * k} x2={vx + vw + g + 14 * k} y1={gy} y2={gy} stroke="var(--muted, #898781)" stroke-width={Math.max(1, k)} />
			<text x={vx + vw + g + 18 * k} y={gy + font * 0.3} font-size={font} fill="var(--muted, #898781)">{mark}</text>
		{/each}
	{/if}

	{#if label}
		<text x={vx + vw / 2} y={box.labelRow.start + 21 * k} text-anchor="middle" font-size={font} font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	{/if}
</svg>

<style>
	/* `width` stays a real attribute, but the rendered box also shrinks under
	   a narrower parent instead of overflowing it. */
	svg {
		max-width: 100%;
		height: auto;
	}

	/* Data may arrive at 10 Hz; short linear transitions turn the steps into
	   continuous motion. */
	.geo {
		transition:
			y 0.18s linear,
			height 0.18s linear;
	}
</style>
