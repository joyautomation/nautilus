<script lang="ts">
	// A valve on a schematic: the bowtie every SCADA package draws, an id under
	// it, and an optional reading under that. Renders an SVG `<g>` for
	// embedding in an `<svg>` you own.
	//
	// Colours: `--glyph-stroke`, `--glyph-fill`, `--glyph-on`, `--glyph-nodata`.
	let {
		cx,
		cy,
		rx = 11,
		ry = 10,
		label = '',
		value = '',
		open = false,
		nodata = false
	}: {
		cx: number;
		cy: number;
		/** Half-width of the bowtie. */
		rx?: number;
		/** Half-height of the bowtie. */
		ry?: number;
		/** Caption under the symbol, usually the valve id. */
		label?: string;
		/** Reading under the caption — a flow, a position. */
		value?: string;
		/** Open (or passing flow): fills the bowtie. */
		open?: boolean;
		/** The point is absent from the frame. */
		nodata?: boolean;
	} = $props();

	const d = $derived(
		`M ${cx - rx} ${cy - ry} L ${cx + rx} ${cy + ry} L ${cx + rx} ${cy - ry} ` +
			`L ${cx - rx} ${cy + ry} Z`
	);
</script>

<g class="glyph valve" class:on={open} class:nodata>
	<path {d} />
	{#if label}<text x={cx} y={cy + ry + 14} class="v-id">{label}</text>{/if}
	{#if value}<text x={cx} y={cy + ry + 24} class="v-val">{value}</text>{/if}
</g>

<style>
	.glyph path {
		fill: var(--glyph-fill, var(--surface));
		stroke: var(--glyph-stroke, var(--ink));
		stroke-width: 1.2;
	}

	.glyph.on path {
		fill: var(--glyph-on, var(--good));
	}

	.glyph.nodata path {
		stroke: var(--glyph-nodata, var(--serious));
		stroke-width: 2;
	}

	.glyph text {
		text-anchor: middle;
		pointer-events: none;
	}

	.v-id {
		font-size: 10px;
		font-weight: 700;
		fill: var(--ink);
	}

	.v-val {
		font-size: 9px;
		fill: var(--ink-2);
		font-family: var(--mono);
	}
</style>
