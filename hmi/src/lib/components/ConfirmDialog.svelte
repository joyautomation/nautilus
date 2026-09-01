<script lang="ts">
	// The confirmation host. Mount it ONCE, next to <Toast/>:
	//
	//     <ConfirmDialog />
	//
	// and every `await confirm({…})` anywhere in the app renders here. There is
	// no per-call-site dialog and no per-call-site `open` boolean; that is the
	// whole point — a confirm step is only worth having if it is impossible to
	// forget, and an imported function is harder to forget than a component.
	//
	// The pattern, from the plan: title as a question, the affected things
	// enumerated (worst first — the caller's order is kept), the operator name
	// editable because the record it writes is unauthenticated and permanent,
	// then [Cancel] [Confirm]. Escape and the backdrop cancel. THE CONFIRM
	// BUTTON IS NEVER THE FOCUSED DEFAULT — Cancel takes focus on open, so a
	// stray Enter cannot command the plant.
	import { onMount, tick } from 'svelte';
	import { confirmState } from '../confirm.svelte.js';
	import { getOperator, setOperator, splitItems } from '../confirm.js';
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';

	let {
		operator = $bindable(''),
		size = 'sm'
	}: {
		/**
		 * The operator name, bindable so a host can seed or observe it. Left
		 * empty it prefills from localStorage on each open and remembers what
		 * the operator types.
		 */
		operator?: string;
		size?: 'sm' | 'md' | 'lg';
	} = $props();

	onMount(() => confirmState.attach());

	const req = $derived(confirmState.current);
	const opts = $derived(req?.options);
	const items = $derived(splitItems(opts?.items, opts?.maxItems));

	let cancelBtn = $state<HTMLElement | null>(null);
	let seededFor = $state(-1);

	// `open` mirrors "is there a question", but has to be BOUND rather than
	// computed: Modal sets it false on Escape/backdrop/×, and if the next
	// queued question is already promoted by then, the computed value would
	// still read `true` and never re-push it — the dialog would stay shut with
	// a promise still pending behind it.
	let open = $state(false);
	$effect(() => {
		open = !!req;
	});

	// Re-seed the name from storage for each NEW question (not on every
	// keystroke), and put focus on Cancel once the dialog is on screen.
	$effect(() => {
		const id = req?.id ?? -1;
		if (id === seededFor) return;
		seededFor = id;
		if (id < 0) return;
		if (!operator) operator = getOperator();
		tick().then(() => cancelBtn?.querySelector('button')?.focus());
	});

	function settle(value: boolean) {
		const id = req?.id;
		if (id === undefined) return;
		if (value && opts?.operator) setOperator(operator.trim());
		confirmState.settleById(id, value);
	}
</script>

<Modal
	bind:open
	{size}
	title={opts?.title ?? ''}
	onclose={() => settle(false)}
	closeOnBackdrop={true}
	closeOnEscape={true}
>
	{#snippet children()}
		{#if opts}
			{#if opts.body}<p class="body">{opts.body}</p>{/if}

			{#if items.shown.length}
				<ul class="items">
					{#each items.shown as line, i (`${i}:${line}`)}
						<li>{line}</li>
					{/each}
					{#if items.more}
						<li class="more">+ {items.more} more</li>
					{/if}
				</ul>
			{/if}

			{#if opts.operator}
				<label class="field">
					<span class="eyebrow">Recorded as</span>
					<input
						type="text"
						bind:value={operator}
						placeholder="operator name"
						autocomplete="off"
						spellcheck="false"
					/>
				</label>
			{/if}

			{#if opts.note}<p class="note">{opts.note}</p>{/if}

			{#if confirmState.waiting > 0}
				<p class="note queued">{confirmState.waiting} more waiting</p>
			{/if}
		{/if}
	{/snippet}

	{#snippet footer()}
		<!-- Cancel first AND focused: the safe answer is the default one. -->
		<span class="focus-holder" bind:this={cancelBtn}>
			<Button variant="ghost" onclick={() => settle(false)}>
				{opts?.cancelLabel ?? 'Cancel'}
			</Button>
		</span>
		<Button variant={opts?.danger ? 'danger' : 'primary'} onclick={() => settle(true)}>
			{opts?.confirmLabel ?? 'Confirm'}
		</Button>
	{/snippet}
</Modal>

<style>
	/* Only there to hold a ref for the focus call — it must not become a
	   flex item of its own in the footer row. */
	.focus-holder {
		display: contents;
	}
	.body {
		margin: 0;
		color: var(--ink-2);
		font-size: var(--font-sm);
	}
	.items {
		margin: 0;
		padding: var(--space-2);
		list-style: none;
		display: grid;
		gap: 2px;
		max-height: 40vh;
		overflow-y: auto;
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		font-size: var(--font-xs);
		color: var(--ink-2);
	}
	.items li {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.items .more {
		color: var(--muted);
		font-style: italic;
	}
	.field {
		display: grid;
		gap: var(--space-1);
	}
	.field input {
		font-size: var(--font-sm);
	}
	.note {
		margin: 0;
		font-size: var(--font-2xs);
		color: var(--muted);
	}
	.note.queued {
		color: var(--warn);
	}
</style>
