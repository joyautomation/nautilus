<script lang="ts">
	// Control valve: a bow-tie body on the line, an actuator on a stem, and a
	// disc indicator that turns with position (0% = closed/perpendicular,
	// 100% = open/inline). The body runs the full width of the box, so the
	// line's ends ARE the box's left and right edges: the body is
	// `data-part="body"` and the default ports sit on its ends at any size.
	// Wider stretches the body; the actuator and the label keep their size.
	import { valveGeometry } from '../drawings.js';
	import type { MimicLook } from '../mimic.js';

	let {
		openPct = 0,
		label = '',
		width = 90,
		height,
		look = 'isa101'
	}: { openPct?: number; label?: string; width?: number; height?: number; look?: MimicLook } = $props();

	const uid = `vlv${Math.random().toString(36).slice(2, 8)}`;

	let box = $derived(valveGeometry(width, height));
	let k = $derived(box.k);

	let open = $derived(Math.max(0, Math.min(100, openPct)));
	let angle = $derived(90 - (open / 100) * 90);

	let stroke = $derived(Math.max(1, 2 * k));
	// Inset by half a stroke so the outline isn't clipped at the box edge.
	let x0 = $derived(stroke / 2);
	let x1 = $derived(box.w - stroke / 2);
	let top = $derived(box.body.y);
	let bot = $derived(box.body.y + box.body.h);
	let cx = $derived(box.w / 2);
	let cy = $derived((top + bot) / 2);

	let real = $derived(look === 'realistic');
	// ISA-101: state by fill, not by hue — an open valve is solid, a closed
	// one hollow. The realistic look keeps the coloured indicator.
	let bodyFill = $derived(real ? `url(#${uid}-steel)` : open > 2 ? 'var(--ink-2, #c3c2b7)' : 'var(--bg, #0d0d0d)');
	let edge = $derived(real ? '#2e3134' : 'var(--ink-2, #c3c2b7)');
	let disc = $derived(
		real ? (open > 2 ? 'var(--s3, #199e70)' : 'var(--serious, #ec835a)') : open > 2 ? 'var(--bg, #0d0d0d)' : 'var(--ink-2, #c3c2b7)'
	);
	let font = $derived(Math.max(8, 12 * k));
</script>

<svg
	viewBox="0 0 {box.w} {box.h}"
	width={box.w}
	height={box.h}
	role="img"
	aria-label={`Valve ${label}: ${open.toFixed(0)}% open`}
>
	{#if real}
		<defs>
			<!-- a horizontal body lit from above -->
			<linearGradient id="{uid}-steel" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0" stop-color="#d4d7db" />
				<stop offset="0.45" stop-color="#9a9ea3" />
				<stop offset="1" stop-color="#4a4d51" />
			</linearGradient>
			<linearGradient id="{uid}-dome" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0" stop-color="#e4e6e9" />
				<stop offset="1" stop-color="#7d8186" />
			</linearGradient>
		</defs>
	{/if}

	<!-- the body's extent, for port anchoring -->
	<rect data-part="body" x="0" y={top} width={box.w} height={bot - top} fill="none" />

	<!-- body: two triangles meeting at the seat -->
	<path class="body" d="M {x0} {top} L {cx} {cy} L {x0} {bot} Z" fill={bodyFill} stroke={edge} stroke-width={stroke} stroke-linejoin="round" />
	<path class="body" d="M {x1} {top} L {cx} {cy} L {x1} {bot} Z" fill={bodyFill} stroke={edge} stroke-width={stroke} stroke-linejoin="round" />
	{#if real}
		<!-- flanges where the body meets the line -->
		<rect x="0" y={top - 3 * k} width={4 * k} height={bot - top + 6 * k} rx={k} fill="#5d6166" />
		<rect x={box.w - 4 * k} y={top - 3 * k} width={4 * k} height={bot - top + 6 * k} rx={k} fill="#5d6166" />
	{/if}

	<!-- actuator on its stem -->
	<line x1={cx} y1={cy} x2={cx} y2={22 * k} stroke={edge} stroke-width={Math.max(1.5, 3 * k)} />
	{#if real}
		<path
			data-part="actuator"
			d="M {cx - 16 * k} {22 * k} L {cx - 16 * k} {16 * k} Q {cx} {2 * k} {cx + 16 * k} {16 * k} L {cx + 16 * k} {22 * k} Z"
			fill="url(#{uid}-dome)"
			stroke="#2e3134"
			stroke-width={stroke}
		/>
	{:else}
		<rect
			data-part="actuator"
			x={cx - 14 * k}
			y={8 * k}
			width={28 * k}
			height={14 * k}
			rx={3 * k}
			fill="var(--surface-2, #232321)"
			stroke={edge}
			stroke-width={stroke}
		/>
	{/if}

	<!-- disc position indicator -->
	<g transform="translate({cx} {cy})">
		<line
			class="disc"
			x1={-9 * k}
			y1="0"
			x2={9 * k}
			y2="0"
			style="transform: rotate({angle}deg)"
			stroke={disc}
			stroke-width={Math.max(2, 4 * k)}
			stroke-linecap="round"
		/>
	</g>

	<text x={cx} y={box.labelRow.start + 16 * k} text-anchor="middle" font-size={font} font-weight="600" fill="var(--ink-2, #c3c2b7)">
		{label ? `${label} · ` : ''}{open.toFixed(0)}%
	</text>
</svg>

<style>
	/* `width` stays a real attribute, but the rendered box also shrinks under
	   a narrower parent instead of overflowing it. */
	svg {
		max-width: 100%;
		height: auto;
	}
	.body {
		transition: fill 0.25s linear;
	}
	.disc {
		transition:
			transform 0.25s linear,
			stroke 0.25s linear;
	}
</style>
