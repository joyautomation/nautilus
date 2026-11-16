<script lang="ts">
	// A pipe segment drawn from an SVG path. When flowing, a dashed overlay
	// marches along the path; speed scales with rate (0–1).
	// Render inside an <svg> element.
	//
	// LOOK. `isa101` draws one line, as a P&ID draws a process line, with the
	// flow dashes on it (no bore: a line has no inside). `realistic`, the
	// default for a pipe drawn on its own, is the walled tube below. <Mimic>
	// passes its look.
	//
	// JUNCTIONS NEED LAYERED RENDERING. A pipe is a wall stroke with a
	// background-coloured bore stroke over it; render pipes one-at-a-time and
	// every tee is a lie — the later pipe's bore punches a slot across the
	// earlier pipe's wall, and a network reads as lines crossing with notches
	// instead of runs merging. The `layer` prop exists so a caller drawing
	// MANY pipes (Mimic, the editor canvas) can paint every wall, then every
	// bore, then every flow overlay: walls union into one casing, bores into
	// one continuous waterway, and a branch whose end sits on another run's
	// centreline merges seamlessly (the 10/6 widths and round caps keep its
	// cap inside the crossing run's bands). Unset = all three layers, the
	// right thing for a standalone pipe.
	//
	// THREE STATES, NOT TWO. A process line is either flowing, standing still,
	// or UNKNOWN — and the third one is not a slower version of the second.
	// `dead` is the pipe whose meter the runtime does not publish (a panel off
	// comms, a point outside the subscription): the run is drawn, because the
	// plant still has that pipe, but the wall goes dashed and dim and no flow
	// can be claimed for it. Painting that as "not flowing" would be the
	// graphic telling a comfortable lie — the same distinction `present: false`
	// carries in a host app's live reads, and the reason it is a prop here
	// rather than something a caller fakes with `flowing={false}`.
	let {
		d,
		flowing = false,
		rate = 1,
		color,
		dead = false,
		layer = undefined,
		look = 'realistic'
	}: {
		d: string;
		flowing?: boolean;
		rate?: number;
		/** The flow dashes' colour. Default: ink on an `isa101` line (motion
		 *  carries the state, colour stays for alarms; `--pipe-flow` sets it),
		 *  the accent blue on a `realistic` tube. */
		color?: string;
		/** No data for this run — dashed, dimmed, never animated. */
		dead?: boolean;
		/** Paint only this stratum (see the junction note above). */
		layer?: 'wall' | 'bore' | 'flow';
		/** `isa101`: one line, as a P&ID draws a process line, the flow
		 * dashes on it. `realistic` (the default, alone): a walled tube. */
		look?: 'isa101' | 'realistic';
	} = $props();
	const line = $derived(look === 'isa101');
	const dash = $derived(color ?? (line ? 'var(--pipe-flow, var(--ink-2, #c3c2b7))' : 'var(--s1, #3987e5)'));

	// Quantize rate into quarter-steps: a continuously varying duration would
	// restart the CSS animation on every data frame and read as jitter.
	let bucket = $derived(Math.max(0.25, Math.ceil(Math.min(rate, 1) * 4) / 4));
	let period = $derived(!dead && flowing && rate > 0.02 ? 0.9 / bucket : 0);
</script>

<g class:dead>
	{#if !layer || layer === 'wall'}
		<path
			class="wall"
			{d}
			fill="none"
			stroke="var(--pipe-wall, var(--edge, #7a7973))"
			stroke-width={line ? 4 : 10}
			stroke-linecap="round"
		/>
	{/if}
	{#if (!layer || layer === 'bore') && !line}
		<path
			class="bore"
			{d}
			fill="none"
			stroke="var(--pipe-bore, var(--bg, #0d0d0d))"
			stroke-width="6"
			stroke-linecap="round"
		/>
	{/if}
	{#if (!layer || layer === 'flow') && period > 0}
		<path
			class="flow"
			style="--period: {period}s"
			{d}
			fill="none"
			stroke={dash}
			stroke-width="4"
			stroke-linecap="round"
			stroke-dasharray="7 9"
		/>
	{/if}
</g>

<style>
	.flow {
		animation: march var(--period) linear infinite;
	}
	@keyframes march {
		to {
			stroke-dashoffset: -16;
		}
	}

	/* No data. The wall breaks up and steps back so a dead run reads as
	   "unknown" at a glance without going invisible — the line is still where
	   the plant put it. Colour is never the only cue (the dash pattern is the
	   signal); `--pipe-dead` lets a host tie it to its own no-data token. */
	.dead .wall {
		stroke: var(--pipe-dead, var(--edge, #7a7973));
		stroke-dasharray: 4 7;
		opacity: 0.5;
	}
	.dead .bore {
		opacity: 0.35;
	}
</style>
