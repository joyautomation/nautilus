<script lang="ts" module>
	/**
	 * The three states a status row colours by, plus the honest fourth.
	 * `unknown` is what an absent or bad-quality point reads as — it must never
	 * collapse into `off`, which is a real, confident "this thing is stopped".
	 */
	export type RowState = 'on' | 'off' | 'fault' | 'unknown';
</script>

<script lang="ts">
	// A one-line device status bar: mode chips, a name, and a value, on a
	// background that carries the state. It is the densest way to show a
	// hundred devices on one screen, and every SCADA package ships a version of
	// it (Ignition's `*Status_Thin` views, FactoryTalk's status banner…).
	//
	// Purely presentational: it takes a resolved `state` rather than a tag, so
	// the same row serves a motor (`RUNST`), a valve (`OPNST`), a bare contact
	// or a level readout. The value cell is a snippet, so the caller drops in
	// whatever readout component it already has.
	//
	// Colours are CSS custom properties, so a legacy port can pin them to the
	// source palette in one place: `--row-on`, `--row-off`, `--row-fault`,
	// `--row-unknown`, `--row-ink`, `--row-chip-bg`, `--row-chip-ink`.
	import type { Snippet } from 'svelte';

	let {
		state = 'unknown',
		label = '',
		auto,
		remote,
		wide = false,
		valueFault = false,
		height = 30,
		title = '',
		style = '',
		value
	}: {
		state?: RowState;
		label?: string;
		/**
		 * Auto vs manual. `undefined`/`null` hides the chip entirely — for a
		 * device with no mode, or a "lite" row that trades the chips for name
		 * width.
		 */
		auto?: boolean | null;
		/** Remote vs local. `undefined`/`null` hides the chip. */
		remote?: boolean | null;
		/** Give the name the full row width instead of a fixed narrow column. */
		wide?: boolean;
		/** Lay a red wash over the value cell — a bad reading beside a good device. */
		valueFault?: boolean;
		/** Row height, px. */
		height?: number;
		/** Hover title, usually the bound tag path. */
		title?: string;
		/** Extra inline style on the row (the caller's layout box). */
		style?: string;
		/** The readout on the right — a value pill, a link, nothing. */
		value?: Snippet;
	} = $props();

	const BG: Record<RowState, string> = {
		on: 'var(--row-on, color-mix(in srgb, var(--good) 55%, var(--surface)))',
		off: 'var(--row-off, var(--surface-2))',
		fault: 'var(--row-fault, color-mix(in srgb, var(--crit) 60%, var(--surface)))',
		unknown: 'var(--row-unknown, color-mix(in srgb, var(--warn) 55%, var(--surface)))'
	};

	const bg = $derived(BG[state] ?? BG.unknown);
</script>

<div class="row" style:background={bg} style:height={`${height}px`} {style} {title}>
	{#if auto !== undefined && auto !== null}
		<span class="chip">{auto ? 'A' : 'M'}</span>
	{/if}
	{#if remote !== undefined && remote !== null}
		<span class="chip">{remote ? 'R' : 'L'}</span>
	{/if}
	<span class="name" class:wide>{label}</span>
	{#if value}
		<span class="pill" class:fault={valueFault}>{@render value()}</span>
	{/if}
</div>

<style>
	.row {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 0 4px;
		border-radius: 4px;
		box-sizing: border-box;
		overflow: visible;
	}

	.chip {
		flex: none;
		min-width: 18px;
		padding: 1px 4px;
		border-radius: 3px;
		background: var(--row-chip-bg, var(--surface-2));
		color: var(--row-chip-ink, var(--ink));
		font-size: 0.8rem;
		font-weight: 500;
		text-align: center;
		line-height: 1.4;
	}

	.name {
		flex: none;
		width: 50px;
		font-size: 14px;
		text-align: center;
		color: var(--row-ink, var(--ink));
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.name.wide {
		flex: 1;
		width: auto;
		text-align: left;
		padding-left: 4px;
	}

	.pill {
		flex: 1;
		min-width: 0;
		display: flex;
		justify-content: flex-end;
		position: relative;
	}

	/* A `value` snippet that rendered nothing this frame must not still claim
	   half the row: a wide name and an empty value cell would split it 50/50.
	   Comments (Svelte's block anchors) don't count against `:empty`, so this
	   catches a snippet whose `{#if}` is currently false. */
	.pill:empty {
		display: none;
	}

	/* A bad reading beside an otherwise-good device: wash the value, keep the
	   row's own state colour intact. */
	.pill.fault::after {
		content: '';
		position: absolute;
		inset: 0;
		background: var(--row-value-fault, var(--crit));
		opacity: 0.5;
		border-radius: 4px;
		pointer-events: none;
	}
</style>
