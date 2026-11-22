<script lang="ts">
	// Operator notes (#720) about one thing (an asset, a tag, an alarm and its
	// occurrence, an area) or the plant's shift log: write a note, see who
	// wrote what and when, change your own within the hour, add an addendum
	// to anyone's. A note is never overwritten or deleted: an edit shows as
	// "edited" (its history is one tap away) and a retraction keeps the text.
	//
	// What a person types is never lost: it is kept as a draft per person and
	// per target until the controller has kept the note, and a sign-out takes
	// the draft away (a kiosk has several people). A write the controller did
	// not keep says why in its own words; one it could not confirm says "may
	// have been kept" and offers to check, never "failed".
	import { onMount, untrack } from 'svelte';
	import {
		NotesClient,
		DraftKeeper,
		notKeptText,
		canChange,
		groupByDay,
		principalChanged,
		targetKey,
		targetLabel,
		type Note,
		type NoteTarget,
		type NoteRevision,
		type SupersededHistory
	} from '../notes.js';
	import { currentAuth } from '../auth.svelte.js';
	import { can, deniedReason } from '../areas.js';
	import { kitFetch } from '../kitfetch.js';
	import { holdsAnywhere, type Me } from '../auth.js';
	import Button from './Button.svelte';

	let {
		client = new NotesClient(),
		drafts = new DraftKeeper(),
		target,
		heading = '',
		grouped = false,
		showSuperseded = true,
		pollMs = 0
	}: {
		client?: NotesClient;
		drafts?: DraftKeeper;
		/** What the notes are about: `{kind: 'log'}` is the shift log. */
		target: NoteTarget;
		heading?: string;
		/** Group by day with handovers on top: the shift log's layout. */
		grouped?: boolean;
		/** Show the notes of a replaced log history, read-only, when there are some. */
		showSuperseded?: boolean;
		/** Re-read every this many ms; 0 reads on mount and after each action. */
		pollMs?: number;
	} = $props();

	let me = $state<Me | null>(null);
	const auth = $derived(currentAuth());
	const who = $derived(auth?.me ?? me);
	const principal = $derived(who?.principal && who.principal !== 'anonymous' ? who.principal : undefined);
	// the alarm's own tag is the controller's to know: for an alarm the page only checks
	// the permission is held somewhere, and the controller decides the area
	const scope = $derived(target.kind === 'tag' ? target.id : undefined);
	// a tag or an alarm's tag within the person's areas; anything else (the shift log,
	// an asset, an area) wherever the permission is held, as the controller decides
	const mayWrite = $derived(!who || !who.auth ? true : scope ? can(who, 'note', scope) : holdsAnywhere(who, 'note'));
	const whyNot = $derived(deniedReason(who, 'note', scope));

	let notes = $state<Note[]>([]);
	let older = $state<Note[]>([]);
	let history = $state<SupersededHistory | undefined>(undefined);
	let loaded = $state(false);
	let loadError = $state('');
	let text = $state('');
	let kind = $state<'note' | 'handover'>('note');
	let re = $state<Note | null>(null);
	let busy = $state(false);
	let message = $state('');
	let unknownId = $state('');
	let unknownText = ''; // what the unconfirmed send held
	let editing = $state<string | null>(null);
	let editText = $state('');
	let retracting = $state<string | null>(null);
	let reason = $state('');
	let openHistory = $state<{ id: string; revs: NoteRevision[] } | null>(null);
	let now = $state(Date.now());

	const groups = $derived(grouped ? groupByDay(notes) : []);

	export async function load() {
		const r = await client.list({ target: target.kind, id: target.id, occurrence: target.occurrence });
		if (!r.ok) {
			loadError = r.outcome === 'invalid' && r.status === 404 ? 'This controller keeps no notes.' : r.message;
			loaded = true;
			return;
		}
		loadError = '';
		notes = r.value;
		loaded = true;
		now = Date.now();
		if (showSuperseded) {
			const s = await client.superseded({ target: target.kind, id: target.id, occurrence: target.occurrence });
			older = s.ok ? s.value.notes : [];
			history = s.ok ? s.value.history : undefined;
		}
	}

	// The draft follows the person (see principalChanged): another person gets
	// their own, a person known late keeps what they typed, and a session that
	// ends takes nothing away (the explicit sign-out clears the drafts, in the
	// kit's sign-out). Drafts need a person: with no auth: block there is
	// nobody to keep one for, and the controller refuses a note then anyway.
	let lastPrincipal: string | undefined;
	let lastTarget: string | undefined;
	$effect(() => {
		const p = principal;
		const tk = targetKey(target);
		untrack(() => {
			if (lastTarget !== undefined && tk !== lastTarget) {
				// the host pointed this panel at another target: its own draft, its own list
				re = null;
				text = drafts.load(p, target);
				void load();
			} else if (p !== lastPrincipal) {
				text = principalChanged(drafts, lastPrincipal, p, target, text);
			}
			lastTarget = tk;
			lastPrincipal = p;
		});
	});

	// every keystroke is kept, except an addendum's (its text would come back
	// as a plain note on this target once the page forgot what it answers)
	$effect(() => {
		const t = text;
		const p = principal;
		const addendum = re !== null;
		untrack(() => {
			if (!addendum) drafts.save(p, target, t);
		});
	});

	onMount(() => {
		void (async () => {
			if (!currentAuth()) {
				try {
					const r = await kitFetch('/api/me');
					if (r.ok) me = (await r.json()) as Me;
				} catch {
					/* the controller decides every write */
				}
			}
			await load();
		})();
		const tick = setInterval(() => (now = Date.now()), 30000); // the author's hour ends on the screen too
		const poll = pollMs > 0 ? setInterval(() => void load(), pollMs) : undefined;
		return () => {
			clearInterval(tick);
			if (poll) clearInterval(poll);
		};
	});

	async function add() {
		if (busy || text.trim() === '') return;
		busy = true;
		message = '';
		unknownId = '';
		const sent = text;
		const body = { target: re ? undefined : target, text: sent, kind: target.kind === 'log' && !re ? kind : undefined, re: re?.id };
		const r = await client.add(body);
		busy = false;
		if (r.ok) {
			// what was typed while the request was in flight is the next note, not this one
			if (text === sent) {
				text = '';
				drafts.clear(principal, target);
			}
			re = null;
			kind = 'note';
			await load();
			return;
		}
		message = notKeptText(r);
		unknownText = sent;
		if (r.outcome === 'unknown' && r.noteId) unknownId = r.noteId;
	}

	async function check() {
		const r = await client.get(unknownId);
		if (r.ok) {
			if (text === unknownText) {
				text = '';
				drafts.clear(principal, target);
			}
			unknownId = '';
			message = 'It was kept.';
			await load();
		} else if (r.status === 404) {
			message = 'It is not there yet. Your text is still here: send it again if you still want it kept.';
			unknownId = '';
		} else {
			message = r.message;
		}
	}

	async function saveEdit(n: Note) {
		busy = true;
		message = '';
		const r = await client.revise(n.id, editText);
		busy = false;
		if (r.ok) {
			editing = null;
			await load();
		} else message = notKeptText(r);
	}

	async function retract(n: Note) {
		busy = true;
		message = '';
		const r = await client.retract(n.id, reason);
		busy = false;
		if (r.ok) {
			retracting = null;
			reason = '';
			await load();
		} else message = notKeptText(r);
	}

	async function toggleHistory(n: Note) {
		if (openHistory?.id === n.id) {
			openHistory = null;
			return;
		}
		const r = await client.get(n.id);
		if (r.ok) openHistory = { id: n.id, revs: r.value.history };
	}

	function when(ms: number): string {
		return new Date(ms).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
	}
	function excerpt(t: string): string {
		return t.length > 60 ? t.slice(0, 60) + '…' : t;
	}
</script>

{#snippet card(n: Note, readonly = false)}
	<li class="note" class:handover={n.kind === 'handover'} data-note={n.id}>
		<div class="meta">
			<b>{n.by}</b>
			<time datetime={new Date(n.at).toISOString()}>{when(n.at)}</time>
			{#if n.kind === 'handover'}<span class="tag hand">handover</span>{/if}
			{#if n.edited}<span class="tag">edited {n.editedAt ? when(n.editedAt) : ''}</span>{/if}
			{#if n.re}<span class="tag">addendum</span>{/if}
			{#if !grouped && target.kind !== n.target.kind}<span class="tag">{targetLabel(n.target)}</span>{/if}
		</div>
		{#if editing === n.id}
			<textarea bind:value={editText} rows="3" maxlength="4096" aria-label="Edit the note"></textarea>
			<div class="row">
				<Button size="sm" variant="primary" disabled={busy || editText.trim() === ''} onclick={() => saveEdit(n)}>Save change</Button>
				<Button size="sm" variant="ghost" onclick={() => (editing = null)}>Cancel</Button>
			</div>
		{:else}
			<p class="text">{n.text}</p>
		{/if}
		{#if retracting === n.id}
			<input type="text" bind:value={reason} maxlength="256" placeholder="Why is it withdrawn?" aria-label="Reason for retracting" />
			<div class="row">
				<Button size="sm" variant="danger" disabled={busy || reason.trim() === ''} onclick={() => retract(n)}>Retract</Button>
				<Button size="sm" variant="ghost" onclick={() => (retracting = null)}>Cancel</Button>
			</div>
		{/if}
		{#if !readonly}
			<div class="row small">
				{#if canChange(n, principal, now) && editing !== n.id}
					<Button size="sm" variant="ghost" onclick={() => ((editing = n.id), (editText = n.text))}>Edit</Button>
					<Button size="sm" variant="ghost" onclick={() => ((retracting = n.id), (reason = ''))}>Retract</Button>
				{/if}
				{#if mayWrite}
					<Button size="sm" variant="ghost" onclick={() => (re = n)}>Add an addendum</Button>
				{/if}
				{#if n.revisions > 1}
					<Button size="sm" variant="ghost" onclick={() => toggleHistory(n)}>{openHistory?.id === n.id ? 'Hide' : 'Show'} history</Button>
				{/if}
			</div>
			{#if openHistory?.id === n.id}
				<ol class="hist">
					{#each openHistory.revs as r (r.rev)}
						<li><time>{when(r.at)}</time> {r.by}{r.retract ? ` retracted it: ${r.reason}` : ''}<p class="text">{r.text}</p></li>
					{/each}
				</ol>
			{/if}
		{/if}
	</li>
{/snippet}

<section class="notes" aria-label={heading || 'Notes'}>
	{#if heading}<h3>{heading}</h3>{/if}

	{#if mayWrite}
		<form
			onsubmit={(e) => {
				e.preventDefault();
				void add();
			}}
		>
			{#if re}
				<p class="re">Addendum to “{excerpt(re.text)}” by {re.by} <button type="button" class="link" onclick={() => (re = null)}>cancel</button></p>
			{/if}
			<textarea bind:value={text} rows="3" maxlength="4096" placeholder={target.kind === 'log' ? 'What should the next shift know?' : `A note about ${targetLabel(target)}`} aria-label="Write a note"></textarea>
			<div class="row">
				{#if target.kind === 'log' && !re}
					<label class="hand"><input type="checkbox" checked={kind === 'handover'} onchange={(e) => (kind = e.currentTarget.checked ? 'handover' : 'note')} /> handover</label>
				{/if}
				<span class="count">{text.length}/4096</span>
				<Button size="sm" variant="primary" disabled={busy || text.trim() === ''} onclick={add}>{re ? 'Add the addendum' : 'Add note'}</Button>
			</div>
		</form>
	{:else if loaded}
		<p class="denied">{whyNot}</p>
	{/if}

	{#if message}
		<p class="msg" role="alert">
			{message}
			{#if unknownId}<Button size="sm" variant="secondary" onclick={check}>Check whether it was kept</Button>{/if}
		</p>
	{/if}
	{#if loadError}<p class="msg" role="alert">{loadError}</p>{/if}

	{#if !loaded}
		<p class="empty">Reading the notes…</p>
	{:else if notes.length === 0 && !loadError}
		<p class="empty">No notes yet.</p>
	{:else if grouped}
		{#each groups as g (g.day)}
			<h4>{new Date(g.day + 'T12:00:00').toLocaleDateString([], { dateStyle: 'full' })}</h4>
			<ul>{#each g.notes as n (n.id)}{@render card(n)}{/each}</ul>
		{/each}
	{:else}
		<ul>{#each notes as n (n.id)}{@render card(n)}{/each}</ul>
	{/if}

	{#if showSuperseded && older.length > 0}
		<details class="older">
			<summary>
				From a superseded history{history?.by ? ` (accepted by ${history.by}` : ' ('}{history?.movedAt ? `${history?.by ? ', ' : ''}moved aside ${when(history.movedAt)}` : ''})
				— {older.length} {older.length === 1 ? 'note' : 'notes'}, read-only
			</summary>
			<p class="empty">A person accepted losing that history's operator state. These notes are a record: they are not part of the live notes and cannot be changed.</p>
			<ul>{#each older as n (n.id)}{@render card(n, true)}{/each}</ul>
		</details>
	{/if}
</section>

<style>
	.notes {
		display: grid;
		gap: 10px;
		font-size: var(--font-sm);
		color: var(--ink-2);
	}
	h3,
	h4 {
		margin: 0;
		color: var(--ink);
		font-size: var(--font-sm);
	}
	h4 {
		font-size: var(--font-xs);
		color: var(--muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		margin-top: 6px;
	}
	form {
		display: grid;
		gap: 6px;
	}
	textarea,
	input[type='text'] {
		width: 100%;
		box-sizing: border-box;
		font: inherit;
		color: var(--ink);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 8px 10px;
		resize: vertical;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
	}
	.row.small {
		margin-top: 2px;
	}
	.count {
		margin-left: auto;
		color: var(--muted);
		font-size: var(--font-xs);
		font-variant-numeric: tabular-nums;
	}
	ul,
	ol {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 8px;
	}
	.note {
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 8px 10px;
		background: var(--surface);
	}
	.note.handover {
		border-color: var(--accent, var(--ink-2));
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 10px;
		align-items: baseline;
		font-size: var(--font-xs);
		color: var(--muted);
	}
	.meta b {
		color: var(--ink);
	}
	.tag {
		border: 1px solid var(--border);
		border-radius: 999px;
		padding: 0 6px;
	}
	.tag.hand {
		color: var(--accent, var(--ink));
		border-color: var(--accent, var(--border));
	}
	.text {
		margin: 4px 0 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		color: var(--ink);
	}
	.hist {
		margin-top: 6px;
		padding-left: 10px;
		border-left: 2px solid var(--border);
		font-size: var(--font-xs);
	}
	.msg {
		margin: 0;
		color: var(--crit);
	}
	.denied,
	.empty,
	.re {
		margin: 0;
		color: var(--muted);
	}
	.link {
		background: none;
		border: 0;
		color: var(--accent, var(--ink));
		cursor: pointer;
		text-decoration: underline;
		font: inherit;
	}
	.hand {
		display: inline-flex;
		gap: 4px;
		align-items: center;
	}
	.older {
		border-top: 1px dashed var(--border);
		padding-top: 8px;
	}
	.older summary {
		cursor: pointer;
		color: var(--muted);
	}
</style>
