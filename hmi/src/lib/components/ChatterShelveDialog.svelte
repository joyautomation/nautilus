<script lang="ts">
	// The one-click fix on a chattering alarm: shelve it for a time, and say
	// why. The duration starts at the suggestion (15 minutes unless the
	// project says otherwise) and the reason is required: one line, written
	// into the audit entry. The shelve ends by itself; nothing here is
	// automatic. Props in, callbacks out; the host makes the call, and the
	// controller still decides who may shelve what.
	import { untrack } from 'svelte';
	import type { AlarmInstance } from '../alarms.svelte.js';
	import { DEFAULT_SHELVE_TIMES_S } from '../alarms.svelte.js';
	import { SHELVE_REASON_MAX, SHELVE_SUGGEST_S, chatterText, reasonProblem } from '../alarmchatter.js';
	import { currentAuth, deniedFor } from '../auth.svelte.js';
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';

	let {
		open = $bindable(false),
		alarm,
		now = Date.now(),
		shelveTimes = DEFAULT_SHELVE_TIMES_S,
		suggest = SHELVE_SUGGEST_S,
		operator = '',
		onconfirm
	}: {
		open?: boolean;
		alarm: AlarmInstance | null;
		now?: number;
		/** Seconds, the picker's choices; the suggestion is added when it is not one of them. */
		shelveTimes?: number[];
		/** The duration the picker opens on, in seconds. */
		suggest?: number;
		/** Prefills the name when nobody is signed in. */
		operator?: string;
		/** `by` is the name to record. */
		onconfirm: (id: string, seconds: number, reason: string, by: string) => void;
	} = $props();

	const times = $derived(shelveTimes.includes(suggest) ? shelveTimes : [...shelveTimes, suggest].sort((a, b) => a - b));
	let seconds = $state(untrack(() => suggest));
	let reason = $state('');
	let byField = $state(untrack(() => operator));
	let touched = $state(false);
	// A fresh dialog each time it opens.
	$effect(() => {
		if (open) {
			seconds = suggest;
			reason = '';
			touched = false;
			byField = operator;
		}
	});

	const problem = $derived(reasonProblem(reason));
	const denied = $derived(alarm ? deniedFor('shelve', alarm.tag) : '');
	const label = (s: number) => (s < 3600 ? `${s / 60} min` : `${s / 3600} h`);

	function confirm() {
		touched = true;
		if (!alarm || problem || denied) return;
		const by = currentAuth()?.user ?? byField;
		onconfirm(alarm.id, seconds, reason.trim(), by);
		open = false;
	}
</script>

<Modal bind:open title={alarm ? `Shelve ${alarm.name}` : 'Shelve'} size="sm">
	{#snippet children()}
		{#if alarm?.chatter}<p class="note">{chatterText(alarm.chatter, now)}</p>{/if}
		<label class="field">
			<span>Shelve for</span>
			<select bind:value={seconds}>
				{#each times as s (s)}
					<option value={s}>{label(s)}{s === suggest ? ' (suggested)' : ''}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			<span>Why (required, one line)</span>
			<input type="text" bind:value={reason} maxlength={SHELVE_REASON_MAX} placeholder="seal weeping, work order for Friday" aria-invalid={touched && !!problem} />
			{#if touched && problem}<span class="bad" role="alert">{problem}</span>{/if}
		</label>
		{#if currentAuth()?.user}
			<p class="note">Recorded as {currentAuth()?.user}, the signed-in user.</p>
		{:else}
			<label class="field">
				<span>Shelved by</span>
				<input type="text" bind:value={byField} placeholder="operator name" />
			</label>
		{/if}
		<p class="note">The alarm comes back by itself when the time is up. Shelving is journalled with your reason.</p>
		{#if denied}<p class="bad" role="alert">{denied}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
		<Button variant="primary" onclick={confirm} disabled={!!denied}>Shelve</Button>
	{/snippet}
</Modal>

<style>
	.field {
		display: grid;
		gap: var(--space-1);
		font-size: var(--font-xs);
		color: var(--ink-2);
		margin-bottom: var(--space-2);
	}
	.field select,
	.field input {
		width: 100%;
	}
	.note {
		margin: 0 0 var(--space-2);
		font-size: var(--font-xs);
		color: var(--muted);
	}
	.bad {
		color: var(--crit);
		font-size: var(--font-xs);
	}
</style>
