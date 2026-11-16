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
	//
	// `marks` are the levels that matter, against the vessel's left wall: an
	// arrow at each one's level and its name and value (a pump's start and
	// stop, a high alarm), from a fixed `value` or a `tag` the screen reads.
	// They draw outside the tank's box, to its left (`marksSide: "right"`:
	// past the scale, on the side the pipes don't reach), as a label would
	// (leave them room), so the vessel keeps its size and its place on its pipes
	// with marks or without. Labels too close to read are nudged apart; their
	// arrows stay at the true level. An `alarm` mark is grey with a hollow
	// arrow until the level reaches it, then turns the alarm colour: alarm
	// colour only while there is an alarm.
	//
	// The liquid is a neutral grey; `alarm` (a bound condition: too hot, out
	// of spec) turns it the alarm colour while it holds.
	import { tankGeometry, type TankShape } from '../drawings.js';
	import { resolveBindings, type MimicLook } from '../mimic.js';

	type Mark = { label: string; value?: number; tag?: string; alarm?: boolean | 'high' | 'low' };

	let {
		levelPct = 50,
		label = '',
		width = 220,
		height,
		look = 'isa101',
		scale = true,
		shape = 'vessel',
		marks = [],
		marksSide = 'left',
		tags = {},
		alarm = false
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
		/** Levels marked on the vessel's left wall, in % of its height: each a
		 *  fixed `value` or a `tag`; `alarm` makes it an alarm level, `"high"`
		 *  (reached from below) or `"low"` (from above): `true` is high when
		 *  the mark is in the tank's upper half, low in its lower half. */
		marks?: Mark[];
		/** Which side of the tank the marks stand on. */
		marksSide?: 'left' | 'right';
		/** The live tag values a mark's `tag` is read from (<Mimic> passes them). */
		tags?: Record<string, unknown>;
		/** The liquid's condition needs attention: it draws in the alarm colour. */
		alarm?: boolean;
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

	// The marks at their levels, top first; a label pushed down from the one
	// above when they would touch.
	let placed = $derived.by(() => {
		const at = (pct: number) => iy + ih - (Math.max(0, Math.min(100, pct)) / 100) * ih;
		const out = marks
			.flatMap((m) => {
				const raw = m.tag ? resolveBindings({ v: m.tag }, tags).v : m.value;
				const pct = typeof raw === 'number' && isFinite(raw) ? raw : undefined;
				if (pct === undefined) return [];
				const high = m.alarm === 'high' || (m.alarm === true && pct >= 50);
				const active = !!m.alarm && (high ? clamped >= pct : clamped <= pct);
				return [{ ...m, pct, active, y: at(pct), ly: at(pct) }];
			})
			.sort((a, b) => a.y - b.y);
		for (let i = 1; i < out.length; i++) out[i].ly = Math.max(out[i].ly, out[i - 1].ly + font * 0.95);
		return out;
	});
	// Graduations thin out on a short vessel so the numbers never collide.
	let grads = $derived(ih < 90 ? [0, 50, 100] : [0, 25, 50, 75, 100]);
	let stroke = $derived(Math.max(1, 2 * k));
	let font = $derived(box.font);

	let real = $derived(look === 'realistic');
	// Neutral, a step off the bore in either theme; the alarm colour while
	// `alarm` holds (a project can set --fluid).
	let liquid = $derived(
		alarm
			? 'color-mix(in srgb, var(--serious, #ec835a) 60%, var(--bg, #0d0d0d))'
			: 'var(--fluid, color-mix(in srgb, var(--ink-2, #c3c2b7) 32%, var(--bg, #0d0d0d)))'
	);
	let name = $derived(pit ? 'Pit' : open ? 'Basin' : 'Tank');
	let shellFill = $derived(real ? (pit ? '#8f8c84' : `url(#${uid}-steel)`) : 'var(--surface-2, #232321)');
	let edge = $derived(real ? '#2e3134' : 'var(--edge, #7a7973)');
	// A pit's ground: the band beside and below it, hatched with the civil
	// drawing's diagonals. Drawn as lines, not an SVG <pattern>: a pattern
	// renders as repeated tiles, and when the drawing is re-rendered at an
	// odd scale (a filter coming or going on a scaled mimic) the tiles'
	// edges show as seams and uneven lines.
	let earthBand = $derived(
		`M ${vx - g} ${vy} H ${vx + vw + g} V ${vy + vh + g} H ${vx - g} Z M ${vx} ${vy} V ${vy + vh} H ${vx + vw} V ${vy} Z`
	);
	let hatch = $derived.by(() => {
		if (!pit) return '';
		const x0 = vx - g, x1 = vx + vw + g, y0 = vy, y1 = vy + vh + g;
		const step = 6 * k * Math.SQRT2; // 6 units apart, measured across the lines
		let d = '';
		// Lines rising to the right (x + y = c), across the band's bounds.
		for (let c = x0 + y0; c <= x1 + y1; c += step) d += `M ${c - y1} ${y1} L ${c - y0} ${y0} `;
		return d;
	});
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
			<!-- the band of ground around the pit, for the hatch to stay in -->
			<clipPath id="{uid}-earth">
				<path d={earthBand} clip-rule="evenodd" />
			</clipPath>
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
		<path d={hatch} clip-path="url(#{uid}-earth)" stroke="var(--muted, #898781)" stroke-width={Math.max(0.75, k)} opacity="0.55" fill="none" />
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
			style:fill={real && !alarm ? `url(#${uid}-liq)` : liquid}
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
		{#each grads as mark (mark)}
			{@const gy = iy + ih - (mark / 100) * ih}
			<line x1={vx + vw + g + 6 * k} x2={vx + vw + g + 14 * k} y1={gy} y2={gy} stroke="var(--muted, #898781)" stroke-width={Math.max(1, k)} />
			<text x={vx + vw + g + 18 * k} y={gy + font * 0.3} font-size={font} fill="var(--muted, #898781)">{mark}</text>
		{/each}
	{/if}

	<!-- level marks: an arrow at the true level, pointing at the tank -->
	{#each placed as m, i (i)}
		{@const c = m.active ? 'var(--serious, #ec835a)' : 'var(--ink-2, #c3c2b7)'}
		{@const ink = m.active ? 'var(--ink, #e6e6e6)' : 'var(--ink-2, #c3c2b7)'}
		{@const r = marksSide === 'right'}
		{@const ax = r ? box.w + 2 * k : vx - g - 2 * k}
		{@const d = r ? 1 : -1}
		<path
			d="M {ax} {m.y} L {ax + d * 8 * k} {m.y - 4 * k} L {ax + d * 8 * k} {m.y + 4 * k} Z"
			fill={m.alarm && !m.active ? 'none' : c}
			stroke={c}
			stroke-width={m.alarm && !m.active ? Math.max(1, k) : 0}
			stroke-linejoin="round"
		/>
		{#if m.ly !== m.y}
			<line x1={ax + d * 8 * k} y1={m.y} x2={ax + d * 12 * k} y2={m.ly} stroke={c} stroke-width={Math.max(0.75, 0.75 * k)} />
		{/if}
		<text x={ax + d * 14 * k} y={m.ly + font * 0.3} font-size={font * 0.9} fill={ink} text-anchor={r ? 'start' : 'end'}
			>{m.label} <tspan font-weight="650">{m.pct.toFixed(0)}</tspan></text
		>
	{/each}

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
		/* level marks draw to the left of the box */
		overflow: visible;
	}

	/* Data may arrive at 10 Hz; short linear transitions turn the steps into
	   continuous motion. */
	.geo {
		transition:
			y 0.18s linear,
			height 0.18s linear;
	}
</style>
