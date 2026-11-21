<script lang="ts">
	// Review and apply an edit (#711, slice 3): the diff of what changed,
	// then Stage -> Validate -> Apply on #706's routes. Nothing is applied
	// without a valid validation; every refusal says what to do.
	import { diffFiles } from '../config/diff.js';
	import { ReviewClient, type Staged } from '../config/review.js';
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

	const key = $derived(JSON.stringify(overlay.files));
	const diffs = $derived(diffFiles(before, overlay.files));
	// An edit after staging makes the staged candidate stale.
	const stale = $derived(!!staged && staged.state !== 'applied' && stagedFor !== key);
	// An applied candidate's result stays up until a new edit makes diffs again.
	const shown = $derived(!!staged && !stale && (staged.state !== 'applied' || diffs.length === 0));
	const v = $derived(staged?.validation);

	async function step(f: () => ReturnType<ReviewClient['stage']>, forKey = key) {
		busy = true;
		const r = await f();
		busy = false;
		if (!r.ok) {
			error = r.message;
			return;
		}
		error = null;
		staged = r.value;
		stagedFor = forKey;
		if (r.value.state === 'applied') onapplied?.(r.value);
	}
	const stage = () => step(() => client.stage(overlay.files));
	const validate = () => staged && step(() => client.validate(staged!.id), stagedFor);
	const apply = () => staged && step(() => client.apply(staged!.id, revert.trim() || undefined), stagedFor);
</script>

<section class="review" aria-label="Review changes">
	{#if error}<p class="msg" role="alert">{error}</p>{/if}
	{#if diffs.length === 0 && staged?.state !== 'applied'}
		<p class="note">No changes to review.</p>
	{:else}
		{#if diffs.length}<h3>Changes</h3>{/if}
		{#each diffs as d (d.path)}
			<pre class="diff" data-path={d.path}>{#each d.diff.split('\n') as l, i (i)}<span class:add={l.startsWith('+') && !l.startsWith('+++')} class:del={l.startsWith('-') && !l.startsWith('---')}>{l}
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
