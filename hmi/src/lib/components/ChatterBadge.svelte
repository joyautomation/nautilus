<script lang="ts">
	// The badge on a chattering alarm: how many times it has come back in this
	// episode, and (title and `detail`) the rule and since when. Words, not
	// colour alone, like the suppression badge. It opens nothing itself; the
	// host decides what a click does (`onclick`), so the badge is the one place
	// the shelve dialog of a later change can hang from.
	import type { AlarmChatter } from '../alarms.svelte.js';
	import { chatterBadge, chatterText } from '../alarmchatter.js';

	let {
		chatter,
		now = Date.now(),
		detail = false,
		onclick
	}: {
		chatter: AlarmChatter;
		now?: number;
		/** Also print the sentence (the asset page), not only in the tooltip. */
		detail?: boolean;
		onclick?: () => void;
	} = $props();

	const text = $derived(chatterText(chatter, now));
</script>

{#if onclick}
	<button type="button" class="chatter" title={text} data-chatter={chatter.activations} aria-haspopup="dialog" onclick={(e) => { e.stopPropagation(); onclick?.(); }}>
		{chatterBadge(chatter)}
	</button>
{:else}
	<span class="chatter" title={text} data-chatter={chatter.activations}>{chatterBadge(chatter)}</span>
{/if}
{#if detail}<span class="detail">{text}</span>{/if}

<style>
	.chatter {
		display: inline-block;
		font: inherit;
		font-size: var(--font-2xs);
		font-weight: 600;
		color: var(--ink);
		background: color-mix(in srgb, var(--warn) 18%, var(--surface));
		border: 1px solid var(--warn);
		border-radius: var(--radius-sm);
		padding: 0 var(--space-1);
		white-space: nowrap;
	}
	button.chatter {
		cursor: pointer;
	}
	button.chatter:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.detail {
		font-size: var(--font-2xs);
		color: var(--muted);
	}
</style>
