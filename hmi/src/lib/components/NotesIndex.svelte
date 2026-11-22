<script lang="ts">
	// Every live note that is about something, newest first, each with what it
	// is about (an asset, a tag, an alarm, an area): the notes the shift log
	// does not show, so a note written on a piece of equipment or an alarm is
	// not lost to anyone who only opens the Log page (#720). Read-only: a note
	// is written with `NotesPanel` on its target. The shift log's own notes
	// (target `log`) are left out; they are the page above it.
	import { onMount } from 'svelte';
	import { NotesClient, targetLabel, type Note } from '../notes.js';

	let {
		client = new NotesClient(),
		pollMs = 15000
	}: {
		client?: NotesClient;
		/** Re-read every this many ms; 0 reads on mount only. */
		pollMs?: number;
	} = $props();

	let notes = $state<Note[]>([]);
	let loaded = $state(false);
	let error = $state('');

	export async function load() {
		const r = await client.list({});
		loaded = true;
		if (!r.ok) {
			error = r.outcome === 'invalid' && r.status === 404 ? '' : r.message; // no notes kept: nothing to list
			return;
		}
		error = '';
		notes = r.value.filter((n) => !n.retracted && n.target.kind !== 'log').sort((a, b) => b.at - a.at);
	}

	onMount(() => {
		void load();
		if (pollMs <= 0) return;
		const t = setInterval(() => {
			if (!document.hidden) void load();
		}, pollMs);
		return () => clearInterval(t);
	});

	function when(ms: number): string {
		return new Date(ms).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short', timeZoneName: 'short' });
	}
</script>

<div class="idx">
	{#if error}<p class="err" role="alert">{error}</p>{/if}
	{#if loaded && !error && notes.length === 0}
		<p class="none">No notes on equipment, tags or alarms.</p>
	{/if}
	<ul>
		{#each notes as n (n.id)}
			<li>
				<div class="meta">
					<strong class="on">{targetLabel(n.target)}</strong>
					<span>{n.by}</span>
					<time datetime={new Date(n.at).toISOString()}>{when(n.at)}</time>
					{#if n.edited}<span class="ed">edited</span>{/if}
				</div>
				<p>{n.text}</p>
			</li>
		{/each}
	</ul>
</div>

<style>
	.idx { display: grid; gap: var(--space-2, 8px); font-size: var(--font-sm, 0.9rem); }
	ul { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--space-2, 8px); }
	li { padding: var(--space-2, 8px) var(--space-3, 12px); border: 1px solid var(--border, color-mix(in srgb, currentColor 15%, transparent)); border-radius: var(--radius-sm, 6px); }
	.meta { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--muted); font-size: var(--font-xs, 0.78rem); }
	.on { color: var(--ink); }
	p { margin: 4px 0 0; white-space: pre-wrap; }
	.none, .err { margin: 0; color: var(--muted); }
	.err { color: var(--crit); }
</style>
