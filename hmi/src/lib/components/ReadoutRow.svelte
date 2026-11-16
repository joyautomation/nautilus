<script lang="ts">
	// A reading as a two-cell row: its name in a shaded cell, then a dark
	// cell with the value, its unit, a sparkline of its last few minutes and
	// its state in words ("Level | 62 % ~ Normal"). Rows of one width
	// stacked under a vessel line up as a table an operator reads at a
	// glance; the name cells keep one width (`--row-name`, default 8em), so
	// the values start in one column.
	//
	//   <ReadoutRow name="Level" value={62.4} unit="%" history={levelBuf}
	//     min={0} max={100} state={{ text: 'Normal' }} />
	//   <ReadoutRow name="Pump Control" value="Lead Pump Pumping Down" />
	//
	// A mimic draws one for a label with `"as": "row"`.
	import Sparkline from './Sparkline.svelte';
	import type { StatusKind } from '../types.js';

	let {
		name,
		value = undefined,
		unit = '',
		decimals = 1,
		history = undefined,
		min = undefined,
		max = undefined,
		state = null,
		width = undefined
	}: {
		/** What the reading is, in an operator's words: "Level", "Flow". */
		name: string;
		/** A number (shown to `decimals`) or words; omitted, the row shows only its state. */
		value?: number | string | null;
		unit?: string;
		decimals?: number;
		/** The value's recent history for the sparkline; omitted, none is drawn. */
		history?: number[];
		/** Fix the sparkline's scale (a level's 0-100); omitted, it fits the data. */
		min?: number;
		max?: number;
		/** The state in words after the value; `kind` colours it only when abnormal. */
		state?: { text: string; kind?: StatusKind } | null;
		/** The row's width in px, so stacked rows line up. Default: its contents. */
		width?: number;
	} = $props();

	const shown = $derived(
		value === undefined || value === null
			? null
			: typeof value === 'number'
				? isFinite(value)
					? value.toFixed(decimals)
					: '—'
				: value || '—'
	);
</script>

<span class="nx-row" style:width={width ? `${width}px` : undefined}
	><span class="name">{name}</span><span class="val"
		>{#if shown !== null}<b class="num">{shown}</b>{#if unit}<span class="unit">{unit}</span>{/if}{/if}{#if history}<span
				class="spark"
				aria-hidden="true"><Sparkline values={history} height={14} yMin={min} yMax={max} color="var(--ink-2, #c3c2b7)" compact /></span
			>{/if}{#if state}<span class="word {state.kind ?? 'off'}" class:alone={shown === null && !history}>{state.text}</span>{/if}</span
	></span
>

<style>
	.nx-row {
		display: inline-grid;
		grid-template-columns: var(--row-name, 8em) 1fr;
		box-sizing: border-box;
		font-size: var(--font-xs);
		line-height: 1.3;
		color: var(--ink, #e6e6e6);
		background: var(--bg, #0d0d0d);
		/* stronger than --border (a panel's faint rule): the row stands on
		   the drawing's ground, so its edge is half way to --edge */
		--row-edge: color-mix(in srgb, var(--edge, #7a7973) 55%, var(--bg, #0d0d0d));
		border: 1px solid var(--row-edge);
		border-radius: 3px;
		overflow: hidden;
		white-space: nowrap;
	}
	.name {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 5px 12px;
		background: var(--surface-2, #2a2a28);
		border-right: 1px solid var(--row-edge);
		font-weight: 650;
	}
	.val {
		display: flex;
		align-items: center;
		gap: 0.5em;
		padding: 5px 12px;
		min-width: 0;
	}
	.num {
		font-weight: 500;
		font-variant-numeric: tabular-nums;
	}
	.unit {
		color: var(--ink-2, #c3c2b7);
	}
	.spark {
		display: block;
		width: 56px;
	}
	.word {
		margin-left: auto;
	}
	.word.alone {
		margin-left: 0;
	}
	.word.warning {
		color: var(--warn, #f5a524);
	}
	.word.serious {
		color: var(--serious, #f76808);
	}
	.word.critical {
		color: var(--crit, #e5484d);
		font-weight: 650;
	}
</style>
