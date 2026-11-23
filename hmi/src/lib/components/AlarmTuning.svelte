<script lang="ts">
	// For the engineer: each chattering alarm with what would have stopped it,
	// replayed from the controller's own journal, and the suggestion as a row
	// to paste into the tuning overlay (alarms.tuning), which is reviewed,
	// committed and applied at the next start. Advisory and read-only: nothing
	// here changes the controller. The limits of the replay are stated on the
	// panel, because a suggestion that hides them reads as more than it is.
	// Props in, callbacks out; the host fetches (`AlarmClient.loadAdvice`).
	import type { AlarmAdviceAnswer } from '../alarms.svelte.js';
	import { PRIORITY_META } from '../alarms.svelte.js';
	import { adviceLine, chatterBadge, delaysText } from '../alarmchatter.js';
	import AlarmSign from './AlarmSign.svelte';
	import Button from './Button.svelte';

	let {
		advice,
		onrefresh
	}: {
		/** `null` until asked for. */
		advice: AlarmAdviceAnswer | null;
		/** Re-reads the suggestions. Omit it to hide the button. */
		onrefresh?: () => void;
	} = $props();

	let copied = $state('');
	async function copy(id: string, row: string) {
		try {
			await navigator.clipboard.writeText(row);
			copied = id;
		} catch {
			copied = ''; // blocked: the row is on screen to select
		}
	}
</script>

<section class="tuning" aria-label="Alarm tuning">
	<header>
		<h3>Chattering alarms: what would help</h3>
		{#if onrefresh}<Button size="sm" variant="secondary" onclick={onrefresh}>Refresh</Button>{/if}
	</header>

	{#if !advice}
		<p class="quiet">Not read yet.</p>
	{:else if !advice.alarms.length}
		<p class="quiet">No alarm is chattering.</p>
	{:else}
		<ul>
			{#each advice.alarms as a (a.id)}
				<li style="--c: {PRIORITY_META[a.priority].color}">
					<div class="head">
						<AlarmSign priority={a.priority} size={12} title={PRIORITY_META[a.priority].label} />
						<b>{a.name}</b>
						<span class="badge">{chatterBadge(a.chatter)}</span>
						<span class="now">runs with {delaysText(a.onDelayMs, a.offDelayMs)}</span>
					</div>
					<p class="why">{adviceLine(a)}</p>
					{#if a.suggestion}
						<p class="detail">
							From {a.suggestion.bounces} activations in the journal; the most in one window goes from
							{a.suggestion.before} to {a.suggestion.after}.
						</p>
					{/if}
					{#if a.row}
						<div class="row">
							<code>{a.row}</code>
							<Button size="sm" variant="ghost" onclick={() => copy(a.id, a.row ?? '')}>
								{copied === a.id ? 'Copied' : 'Copy row'}
							</Button>
						</div>
					{/if}
				</li>
			{/each}
		</ul>
		<p class="note">{advice.note}</p>
		<p class="note">
			To apply: add the row to the file named by <code>alarms.tuning</code> in the project, run
			<code>naut check</code>, commit it, and restart the controller. Nothing changes until then.
		</p>
	{/if}
</section>

<style>
	.tuning {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		font-size: var(--font-sm);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	h3 {
		margin: 0;
		font-size: var(--font-sm);
	}
	ul {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	li {
		display: grid;
		gap: var(--space-1);
		padding: var(--space-2);
		border: 1px solid var(--border);
		border-left: 3px solid var(--c);
		border-radius: var(--radius-sm);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
	}
	.badge {
		font-size: var(--font-2xs);
		font-weight: 600;
		border: 1px solid var(--warn);
		border-radius: var(--radius-sm);
		padding: 0 var(--space-1);
	}
	.now,
	.detail,
	.note,
	.quiet {
		margin: 0;
		color: var(--muted);
		font-size: var(--font-xs);
	}
	.why {
		margin: 0;
		color: var(--ink);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	code {
		font-family: var(--mono);
		font-size: var(--font-xs);
		overflow-wrap: anywhere;
		user-select: all;
	}
</style>
