<script lang="ts">
	// The key to the alarm signs: each priority's shape and what it asks of
	// the operator. A page shows it while anything is in alarm.
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { PRIORITIES, PRIORITY_SIGN } from '../alarms.js';
	import AlarmSign from './AlarmSign.svelte';

	let { levels = PRIORITIES.slice(0, 4), palette = undefined }: { levels?: string[]; palette?: Palette } = $props();
	const ctx = getContext<Palette | undefined>('hmi3d:palette');
	let p = $derived(palette ?? ctx ?? DEFAULT_PALETTE);
</script>

<ul class="key" aria-label="Alarm priorities">
	{#each levels as lvl}
		<li><AlarmSign priority={lvl} color={p.priority[lvl]} size={16} /><span><b>{lvl}</b> {PRIORITY_SIGN[lvl].urgency}</span></li>
	{/each}
</ul>

<style>
	.key {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 4px;
		font: 12px/1.3 system-ui, sans-serif;
	}
	li {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	b {
		font-weight: 600;
		text-transform: capitalize;
	}
</style>
