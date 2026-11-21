<script lang="ts">
	// Review and apply an edit (#711, slice 3): the diff of what changed,
	// then Stage -> Validate -> Apply on #706's routes. Nothing is applied
	// without a valid validation; every refusal says what to do.
	import { diffFiles } from '../config/diff.js';
	import { ReviewClient, type Committed, type ReviewResult, type Staged } from '../config/review.js';
	import type { Overlay } from '../config/form.js';
	import Button from './Button.svelte';

	let {
		before,
		overlay,
		client = new ReviewClient(),
		onapplied
	}: {
		/** What runs: path to text (ConfigFiles.files). */
		before: Record<string, string>;
		overlay: Overlay;
		client?: ReviewClient;
		onapplied?: (s: Staged) => void;
	} = $props();

	let staged = $state<Staged | null>(null);
	let stagedFor = $state('');
	let busy = $state(false);
	let error = $state<string | null>(null);
	let revert = $state('');
	let message = $state('');
	let committed = $state<{ id: string; c: Committed } | null>(null);
	let canPatch = $state(false);

	const key = $derived(JSON.stringify(overlay.files));
	const diffs = $derived(diffFiles(before, overlay.files));
	// An edit after staging makes the staged candidate stale.
	const stale = $derived(!!staged && staged.state !== 'applied' && stagedFor !== key);
	// An applied candidate's result stays up until a new edit makes diffs again.
	const shown = $derived(!!staged && !stale && (staged.state !== 'applied' || diffs.length === 0));
	const v = $derived(staged?.validation);

	async function step(f: () => Promise<ReviewResult>, forKey = key) {
		busy = true;
		let r;
		try {
			r = await f();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			return;
		} finally {
			busy = false;
		}
		if (!r.ok) {
			error = r.message;
			// A refused step may still have moved the candidate (failed, invalid).
			if (r.staged) {
				staged = r.staged;
				stagedFor = forKey;
			}
			return;
		}
		error = null;
		staged = r.value;
		stagedFor = forKey;
		if (r.value.state === 'applied') onapplied?.(r.value);
	}
	// Saving what runs: a git commit where the controller has a checkout,
	// else the patch (the server's 409 says which, and names the other way).
	async function commit() {
		if (!staged) return;
		busy = true;
		try {
			const r = await client.commit(staged.id, message.trim() || undefined);
			if (r.ok) { committed = { id: staged.id, c: r.value }; error = null; canPatch = false; } else { error = r.message; canPatch = r.status === 409; }
		} finally { busy = false; }
	}
	async function patch() {
		busy = true;
		try {
			const r = await client.patch();
			if (!r.ok) { error = r.message; return; }
			error = null;
			const a = document.createElement('a');
			const url = URL.createObjectURL(new Blob([r.patch], { type: 'text/x-diff' }));
			a.href = url; a.download = 'nautilus-config.patch'; a.click();
			URL.revokeObjectURL(url);
		} finally { busy = false; }
	}
	const stage = () => step(() => client.stage(overlay.files));
	const validate = () => staged && step(() => client.validate(staged!.id), stagedFor);
	const apply = () => staged && step(() => client.apply(staged!.id, revert.trim() || undefined), stagedFor);
</script>

<section class="review" aria-label="Review changes" aria-live="polite">
	{#if error}<p class="msg" role="alert">{error}</p>{/if}
	{#if diffs.length === 0 && staged?.state !== 'applied'}
		<p class="note">No changes to review.</p>
	{:else}
		{#if diffs.length}<h3>Changes</h3>{/if}
		{#each diffs as d (d.path)}
			<pre class="diff" data-path={d.path}>{#each d.diff.split('\n') as l, i (i)}<span class:add={i > 1 && l.startsWith('+')} class:del={i > 1 && l.startsWith('-')}>{l}
</span>{/each}</pre>
		{/each}
		{#if diffs.length}<div class="actions">
			<Button size="sm" disabled={busy} onclick={stage}>{staged && shown ? 'Stage again' : 'Stage'}</Button>
			<Button size="sm" disabled={busy || !staged || stale || !shown || staged.state === 'applied'} onclick={validate}>Validate</Button>
			<label class="note">Revert if silent <input bind:value={revert} placeholder="e.g. 10s" size="6" /></label>
			<Button size="sm" variant="primary" disabled={busy || !staged || stale || !shown || staged.state !== 'valid'} onclick={apply}>Apply</Button>
		</div>{/if}
		{#if stale}<p class="note">The edit changed after it was staged. Stage it again.</p>{/if}
		{#if staged && shown}
			<p class="state" data-state={staged.state}>Candidate {staged.id}: {staged.state}{staged.error ? ` — ${staged.error}` : ''}</p>
			{#if v}
				<ul class="checks">
					{#each v.checks as c (c.name)}
						<li class:bad={!c.ok}>{c.skipped ? '·' : c.ok ? '✓' : '✗'} {c.name}{c.detail ? `: ${c.detail}` : ''}</li>
					{/each}
					{#each v.probes ?? [] as p (p.driver + '/' + (p.device ?? ''))}
						<li class:bad={!p.ok}>{p.ok ? '✓' : '✗'} probe {p.driver}{p.device ? '/' + p.device : ''} ({p.mode}: {p.why}){p.detail ? ` — ${p.detail}` : ''}</li>
					{/each}
				</ul>
				{#if v.changes.length}<p class="note">Changes: {v.changes.map((c) => c.driver + (c.device ? '/' + c.device : '')).join(', ')}</p>{/if}
				{#if v.willCommand.length}<p class="note">The first scan will command: {v.willCommand.map((c) => `${c.tag}=${JSON.stringify(c.value)}`).join(', ')}</p>{/if}
				{#each v.notes ?? [] as n (n)}<p class="note">{n}</p>{/each}
			{/if}
			{#if staged.result}
				<p class="note" data-result>
					Applied. {staged.result.kept ? 'Kept across a restart.' : `Not kept: ${staged.result.keptWhy ?? 'unknown'}.`}
					{#if staged.result.pending?.length}Waiting on {staged.result.pending.join(', ')}.{/if}
					{#if staged.result.answered?.length}Answered: {staged.result.answered.join(', ')}.{/if}
				</p>
				{#if committed?.id === staged.id}
					<p class="note" data-committed>Committed {committed.c.commit.slice(0, 12)} on {committed.c.branch}: {committed.c.files.join(', ')}.</p>
				{:else}
					<div class="actions">
						<input bind:value={message} placeholder="Commit message (optional)" size="28" aria-label="Commit message" />
						<Button size="sm" disabled={busy} onclick={commit}>Commit</Button>
						<Button size="sm" disabled={busy} onclick={patch}>{canPatch ? 'Download patch instead' : 'Download patch'}</Button>
					</div>
				{/if}
			{/if}
		{/if}
	{/if}
</section>

<style>
	.review { display: grid; gap: var(--space-2); font-size: var(--font-sm); }
	h3 { margin: var(--space-3) 0 0; font-size: var(--font-md); }
	.diff { margin: 0; padding: var(--space-2); overflow: auto; font-size: var(--font-xs); border: 1px solid color-mix(in srgb, currentColor 15%, transparent); border-radius: var(--radius-sm); }
	.add { color: var(--ok, green); }
	.del { color: var(--crit); }
	.actions { display: flex; gap: var(--space-2); align-items: center; flex-wrap: wrap; }
	.checks { margin: 0; padding-left: var(--space-3); list-style: none; }
	.bad { color: var(--crit); }
	.note, .state { margin: 0; color: var(--muted); font-size: var(--font-xs); }
	.msg { margin: 0; padding: var(--space-2) var(--space-3); border: 1px solid var(--crit); color: var(--crit); border-radius: var(--radius-sm); }
</style>
