<script lang="ts">
	// A level-only tank — a municipal reservoir, a clearwell, a wet well.
	//
	// This is NOT the kit's `Tank`, which is the heated-vessel digital twin
	// (`tempC` / `heaterPct`, liquid coloured by temperature, an animated
	// heater coil). Both exist because they answer different questions: that
	// one asks "how hot is this batch", this one asks "how much water is left
	// and how close is it to overflowing".
	//
	// `value` is in ENGINEERING UNITS (feet, metres, whatever `units` says) and
	// is scaled against `min`…`max`. `undefined` means the point is not
	// published: no liquid, no percentage, the no-data outline — never a
	// confident zero, which on a reservoir is the difference between "full
	// enough" and "call somebody".
	//
	// `marks` are alarm/operating bands in those same units, so the operator
	// reads the level against the setpoints they were trained on rather than
	// against a bare scale.
	import TankGlyph from './TankGlyph.svelte';

	let {
		value,
		min = 0,
		max = 100,
		units = 'ft',
		precision = 1,
		label = '',
		width = 150,
		height = 118,
		fill = 'var(--s1)',
		marks = [],
		overflow,
		volume,
		volumeUnits = 'gal',
		showPercent = true,
		sub = ''
	}: {
		/** The level, in `units`. `undefined` = not published. */
		value?: number;
		/** Bottom of the scale (an empty vessel). */
		min?: number;
		/** Top of the scale — the overflow elevation, usually. */
		max?: number;
		units?: string;
		precision?: number;
		/** Caption under the tank. */
		label?: string;
		width?: number;
		height?: number;
		/** Liquid colour — drive it from the band the level is in. */
		fill?: string;
		/** Band marks in engineering units (low-low, low, high, overflow…). */
		marks?: number[];
		/** Draws a heavier mark at this level, in units — the overflow line. */
		overflow?: number;
		/** Stored volume, if the model knows the vessel's geometry. */
		volume?: number;
		volumeUnits?: string;
		/** Show the percentage of range in the top-right corner. */
		showPercent?: boolean;
		/** Bottom sub-line, when there is no volume to show. */
		sub?: string;
	} = $props();

	const span = $derived(max > min ? max - min : 1);
	const present = $derived(typeof value === 'number' && Number.isFinite(value));
	const frac = $derived(present ? Math.max(0, Math.min(1, ((value as number) - min) / span)) : undefined);

	const toFrac = (v: number) => (v - min) / span;
	const allMarks = $derived([...marks, ...(overflow === undefined ? [] : [overflow])].map(toFrac));

	const pctText = $derived(
		showPercent && frac !== undefined ? `${Math.round(frac * 100)}%` : ''
	);
	const valText = $derived(
		present ? `${(value as number).toFixed(precision)} / ${max.toFixed(precision)} ${units}` : 'no data'
	);
	const subText = $derived(
		volume !== undefined && Number.isFinite(volume)
			? `${Math.round(volume).toLocaleString()} ${volumeUnits}`
			: sub
	);
</script>

<div class="lt" style:width={`${width}px`}>
	<svg
		viewBox={`0 0 ${width} ${height}`}
		{width}
		role="img"
		aria-label={`${label || 'Tank'}: ${valText}`}
	>
		<TankGlyph
			x={1}
			y={1}
			w={width - 2}
			h={height - 2}
			level={frac}
			{fill}
			marks={allMarks}
			corner={pctText}
			value={valText}
			sub={subText}
		/>
	</svg>
	{#if label}<div class="cap">{label}</div>{/if}
</div>

<style>
	.lt {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 2px;
		/* `width` stays a real attribute (the inline style above, and the svg's
		   own attr/viewBox), but the rendered box also shrinks under a
		   narrower parent instead of overflowing it. */
		max-width: 100%;
	}

	svg {
		display: block;
		max-width: 100%;
		height: auto;
		font-family: var(--font);
	}

	.cap {
		font-size: var(--font-2xs);
		color: var(--ink-2);
		text-align: center;
	}
</style>
