<script lang="ts">
	// A pump on a schematic: the ISA circle, an id inside it, and a reading
	// underneath. Renders an SVG `<g>` for embedding in an `<svg>` you own.
	//
	// The reading goes BELOW the circle rather than beside it on purpose — on a
	// fleet view pumps sit on a tight pitch, and a four-digit flow set beside
	// one lands on top of the next pump in the row.
	//
	// Colours: `--glyph-stroke`, `--glyph-off`, `--glyph-on`, `--glyph-nodata`.
	let {
		cx,
		cy,
		r = 9,
		label = '',
		value = '',
		running = false,
		nodata = false,
		disabled = false
	}: {
		cx: number;
		cy: number;
		r?: number;
		/** Short id drawn inside the circle. */
		label?: string;
		/** Reading drawn under the circle — omit it while the pump is idle. */
		value?: string;
		running?: boolean;
		/** The point is absent from the frame — outline, don't guess a state. */
		nodata?: boolean;
		/** Out of service in the model: dashed and dimmed, not an alarm. */
		disabled?: boolean;
	} = $props();
</script>

<g class="glyph pump" class:on={running} class:nodata class:disabled>
	<circle {cx} {cy} {r} />
	{#if label}<text x={cx} y={cy + r / 3} class="p-id">{label}</text>{/if}
	{#if value}<text x={cx} y={cy + r + 8} class="p-val">{value}</text>{/if}
</g>

<style>
	.glyph circle {
		fill: var(--glyph-off, transparent);
		stroke: var(--glyph-stroke, var(--ink));
		stroke-width: 1.2;
	}

	.glyph.on circle {
		fill: var(--glyph-on, var(--good));
	}

	.glyph.disabled circle {
		stroke-dasharray: 2 2;
		opacity: 0.5;
	}

	.glyph.nodata circle {
		stroke: var(--glyph-nodata, var(--serious));
		stroke-width: 2;
	}

	.glyph text {
		text-anchor: middle;
		pointer-events: none;
	}

	.p-id {
		font-size: 8px;
		fill: var(--ink);
		font-weight: 600;
	}

	.p-val {
		font-size: 8px;
		fill: var(--ink-2);
		font-family: var(--mono);
	}
</style>
