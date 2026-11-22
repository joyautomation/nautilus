<script lang="ts">
	// Under the alarm bar, shown only while the controller reports a flood:
	// "Alarm flood: 23 in 10 min (limit 10)", the first alarms of it in time
	// order (the best free guess at the cause), the equipment that raised most
	// of it, and a Focus toggle. Props in, callbacks out; it fetches nothing
	// and never acknowledges or hides an alarm. Focus only asks the table
	// (through `onfocus`) to put what needs a person first.
	import type { AlarmFlood } from '../alarms.svelte.js';
	import { PRIORITY_META } from '../alarms.svelte.js';
	import { floodText, groupsText, spanText } from '../alarmchatter.js';
	import AlarmSign from './AlarmSign.svelte';
	import Button from './Button.svelte';

	let {
		flood,
		now = Date.now(),
		focused = false,
		onfocus,
		journalHref
	}: {
		/** `null`/`undefined` when there is no flood: renders nothing. */
		flood?: AlarmFlood | null;
		now?: number;
		focused?: boolean;
		/** Toggles focus. Omit it to hide the toggle. */
		onfocus?: (on: boolean) => void;
		/** Where the sequence of events is (the history page); a link when set. */
		journalHref?: string;
	} = $props();

	const ago = (ms: number) => `${spanText(Math.max(0, now - ms))} ago`;
</script>

{#if flood}
	<section class="flood" role="status" aria-label="Alarm flood" data-flood={flood.id}>
		<div class="head">
			<b>{floodText(flood)}</b>
			<span class="since">for {spanText(now - flood.sinceMs)}; {flood.total} so far, peak {flood.peak}</span>
			{#if onfocus}
				<Button size="sm" variant={focused ? 'primary' : 'secondary'} onclick={() => onfocus(!focused)} aria-pressed={focused}>
					{focused ? 'Focus on' : 'Focus'}
				</Button>
			{/if}
			{#if journalHref}<a href={journalHref}>Sequence of events</a>{/if}
		</div>
		{#if flood.firstOut?.length}
			<ol class="first" aria-label="First out">
				{#each flood.firstOut as a (a.id)}
					<li style="--c: {PRIORITY_META[a.priority].color}">
						<AlarmSign priority={a.priority} size={12} title={PRIORITY_META[a.priority].label} />
						<span class="name">{a.name}</span>
						<span class="age">{ago(a.ms)}</span>
					</li>
				{/each}
			</ol>
		{/if}
		{#if flood.groups?.length}
			<p class="groups">Most from: {groupsText(flood.groups)}</p>
		{/if}
	</section>
{/if}

<style>
	.flood {
		display: grid;
		gap: var(--space-1);
		padding: var(--space-2) var(--space-3);
		background: color-mix(in srgb, var(--serious) 12%, var(--surface));
		border: 1px solid var(--serious);
		border-radius: var(--radius);
		color: var(--ink);
		font-size: var(--font-sm);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
	}
	.since {
		color: var(--ink-2);
		font-size: var(--font-xs);
	}
	.first {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.first li {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		color: var(--c);
		min-width: 0;
	}
	.name {
		color: var(--ink);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.age {
		color: var(--muted);
		font-size: var(--font-2xs);
	}
	.groups {
		margin: 0;
		color: var(--ink-2);
		font-size: var(--font-xs);
	}
</style>
