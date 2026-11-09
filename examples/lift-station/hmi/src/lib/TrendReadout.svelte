<script module lang="ts">
	// History per readout, kept at module level so it survives the page
	// remounting it (switching to 3D and back): one sample a second, the
	// last five minutes.
	const SAMPLES = 300;
	const histories = new Map<string, number[]>();
</script>

<script lang="ts">
	// A bound value as a readout chip, like the mimic's own bound labels,
	// with a sparkline of its last few minutes beside the number. The
	// mimic binds a value, not a series, so the component keeps the series
	// itself: it samples `value` once a second under `text` (each readout's
	// own history). App-supplied via <Mimic registry>, not part of the kit.
	import { onMount } from 'svelte';
	import { Sparkline } from '@joyautomation/nautilus-hmi';

	let {
		text = '',
		value,
		unit = '',
		decimals = 1,
		min,
		max
	}: {
		text?: string;
		value?: number;
		unit?: string;
		decimals?: number;
		/** Fix the sparkline's scale (a level's 0-100); omitted, it fits the data. */
		min?: number;
		max?: number;
	} = $props();

	// The readout's name is fixed for its life on the page.
	// svelte-ignore state_referenced_locally
	let series = $state<number[]>(histories.get(text) ?? []);
	const shown = $derived(typeof value === 'number' && isFinite(value) ? value.toFixed(decimals) : '—');

	onMount(() => {
		const id = setInterval(() => {
			if (typeof value !== 'number' || !isFinite(value)) return;
			const next = [...series, value].slice(-SAMPLES);
			histories.set(text, next);
			series = next;
		}, 1000);
		return () => clearInterval(id);
	});
</script>

<span class="readout">
	{#if text}<span class="t">{text}</span>{/if}
	<b>{shown}</b>{#if unit}<span class="u">{unit}</span>{/if}
	<span class="spark" aria-hidden="true">
		<Sparkline values={series} height={16} yMin={min} yMax={max} compact />
	</span>
</span>

<style>
	.readout {
		display: inline-flex;
		align-items: center;
		gap: 0.35em;
		padding: 2px 7px;
		font-size: var(--font-2xs, 11px);
		color: var(--muted, #8b949e);
		background: var(--surface-2, #232321);
		border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		border-radius: 6px;
		white-space: nowrap;
	}
	b {
		color: var(--ink, #e6e6e6);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}
	.spark {
		display: block;
		width: 40px;
		margin-left: 0.3em;
	}
</style>
