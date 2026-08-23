<script lang="ts">
	// A tank on a schematic: a box, the liquid inside it, optional setpoint
	// marks, and four corners of text. Renders an SVG `<g>`, so it goes INSIDE
	// an `<svg>` you already own — a network diagram, a P&ID, a fleet view. For
	// a standalone tank with its own box and formatting, use `LevelTank`.
	//
	// `level` is a FRACTION, and `undefined` means the point is not published.
	// That distinction is the whole design: an absent level draws an empty
	// vessel with the no-data outline, never a confident zero, because on a
	// reservoir those two readings mean opposite things.
	//
	// Colours: `--glyph-stroke`, `--glyph-fill`, `--glyph-body`,
	// `--glyph-mark`, `--glyph-nodata`.
	let {
		x,
		y,
		w,
		h,
		level,
		fill = 'var(--s1)',
		fillOpacity = 0.45,
		marks = [],
		id = '',
		corner = '',
		value = '',
		sub = ''
	}: {
		x: number;
		y: number;
		w: number;
		h: number;
		/** Fill fraction, 0…1. `undefined` = not published — no liquid, no-data
		 *  outline. */
		level?: number;
		/** Liquid colour — drive it from the alarm band the level is in. */
		fill?: string;
		fillOpacity?: number;
		/** Setpoint lines, as fractions of the vessel's depth (0…1). */
		marks?: number[];
		/** Top-left caption, usually the tank id. */
		id?: string;
		/** Top-right caption, usually the percentage. */
		corner?: string;
		/** Bottom line — the reading in engineering units. */
		value?: string;
		/** Bottom sub-line — a volume, an owner, whatever is left over. */
		sub?: string;
	} = $props();

	const present = $derived(typeof level === 'number' && Number.isFinite(level));
	const frac = $derived(present ? Math.max(0, Math.min(1, level as number)) : 0);
</script>

<g class="glyph tank" class:nodata={!present}>
	<rect {x} {y} width={w} height={h} rx="3" class="body" />
	<rect
		class="liquid"
		x={x + 1}
		y={y + 1 + (h - 2) * (1 - frac)}
		width={w - 2}
		height={(h - 2) * frac}
		{fill}
		opacity={fillOpacity}
	/>
	{#each marks as m, i (i)}
		{@const my = y + h * (1 - Math.max(0, Math.min(1, m)))}
		<line x1={x} x2={x + w} y1={my} y2={my} class="sp" />
	{/each}
	{#if id}<text x={x + 6} y={y + 15} class="t-id">{id}</text>{/if}
	{#if corner}<text x={x + w - 6} y={y + 15} class="t-corner">{corner}</text>{/if}
	{#if value}<text x={x + 6} y={y + h - 18} class="t-val">{value}</text>{/if}
	{#if sub}<text x={x + 6} y={y + h - 6} class="t-sub">{sub}</text>{/if}
</g>

<style>
	.glyph .body {
		fill: var(--glyph-body, var(--surface));
		stroke: var(--glyph-stroke, var(--ink));
		stroke-width: 1.6;
	}

	/* The liquid's colour is the `fill` ATTRIBUTE — deliberately not styled
	   here, because a CSS `fill` would win over the attribute and silently
	   erase whatever band colour the caller passed. */
	.glyph .liquid {
		stroke: none;
	}

	.glyph text {
		pointer-events: none;
	}

	.sp {
		stroke: var(--glyph-mark, var(--s2));
		stroke-width: 0.8;
		stroke-dasharray: 3 3;
		opacity: 0.6;
	}

	.t-id {
		font-size: 11px;
		font-weight: 700;
		fill: var(--ink);
		text-anchor: start;
	}

	.t-corner {
		font-size: 11px;
		font-weight: 700;
		fill: var(--ink-2);
		text-anchor: end;
	}

	.t-val {
		font-size: 10px;
		fill: var(--ink);
		text-anchor: start;
		font-family: var(--mono);
	}

	.t-sub {
		font-size: 9px;
		fill: var(--muted);
		text-anchor: start;
	}

	/* No data: the point is absent from the frame. An outline, not a colour
	   change, so it survives whatever the liquid colour is doing. */
	.nodata .body {
		stroke: var(--glyph-nodata, var(--serious));
		stroke-width: 2;
	}
</style>
