<script lang="ts">
	// A text input with an intellisense-style dropdown: free text always
	// wins — suggestions are offers, never gates. Focus/click opens the
	// FULL vocabulary (scrollable combobox — otherwise the options are
	// undiscoverable); typing filters it. ArrowUp/Down highlight (and
	// reopen a dismissed list), Tab accepts (first item if none
	// highlighted), Enter accepts only a highlighted item (otherwise it
	// falls through to the caller's commit), Escape dismisses the list
	// first and only then reaches the caller.
	import { filterItems, lastToken, leadingIdent, replaceLastToken, replaceLeadingIdent, replaceTailWord, tailWord, type SuggestItem } from './suggest';

	let {
		value = $bindable(''),
		items = [],
		multi = false,
		call = false,
		tail = false,
		cls = '',
		style = '',
		onkeydown,
		onblur
	}: {
		value?: string;
		items?: SuggestItem[];
		multi?: boolean;
		/** call mode: complete the leading FUNCTION NAME of "FN(args)",
		 * never touching the argument list. */
		call?: boolean;
		/** tail mode: complete the LAST word ("N Pu" → "N PumpRun"),
		 * keeping what comes before it (an SFC action's qualifier). */
		tail?: boolean;
		cls?: string;
		style?: string;
		onkeydown?: (ev: KeyboardEvent) => void;
		onblur?: (ev: FocusEvent) => void;
	} = $props();

	let el = $state<HTMLInputElement | null>(null);
	let listEl = $state<HTMLDivElement | null>(null);
	let focused = $state(false);
	let dismissed = $state(false);
	// Until the user types, the list is a browsable combobox showing the
	// whole vocabulary; the first keystroke switches it to filtering.
	let typed = $state(false);
	let active = $state(-1);
	const token = $derived(call ? leadingIdent(value) : tail ? tailWord(value) : multi ? lastToken(value) : value);
	const filtered = $derived.by(() => {
		if (!focused || dismissed) return [];
		return typed ? filterItems(items, token.trim()) : items;
	});
	// Keep the highlighted row visible as arrows walk a scrolled list.
	$effect(() => {
		if (active >= 0) listEl?.children[active]?.scrollIntoView({ block: 'nearest' });
	});

	export function focus() {
		el?.focus();
		el?.select();
	}

	function reopen() {
		dismissed = false;
		typed = false;
		active = -1;
	}

	function accept(i: number) {
		const it = filtered[i];
		if (!it) return;
		value = call ? replaceLeadingIdent(value, it.name) : tail ? replaceTailWord(value, it.name) : multi ? replaceLastToken(value, it.name) : it.name;
		dismissed = true;
		active = -1;
		el?.focus();
	}

	function keydown(ev: KeyboardEvent) {
		if (!filtered.length && ev.key === 'ArrowDown' && items.length) {
			reopen();
			ev.preventDefault();
			ev.stopPropagation();
			return;
		}
		if (filtered.length) {
			if (ev.key === 'ArrowDown') {
				active = (active + 1) % filtered.length;
				ev.preventDefault();
				ev.stopPropagation();
				return;
			}
			if (ev.key === 'ArrowUp') {
				active = active <= 0 ? filtered.length - 1 : active - 1;
				ev.preventDefault();
				ev.stopPropagation();
				return;
			}
			if (ev.key === 'Tab') {
				accept(active === -1 ? 0 : active);
				ev.preventDefault();
				ev.stopPropagation();
				return;
			}
			if (ev.key === 'Enter' && active !== -1) {
				accept(active);
				ev.preventDefault();
				ev.stopPropagation();
				return;
			}
			if (ev.key === 'Escape') {
				dismissed = true;
				active = -1;
				ev.preventDefault();
				ev.stopPropagation();
				return;
			}
		}
		onkeydown?.(ev);
	}
</script>

<span class="suggest {cls}" {style}>
	<input
		bind:this={el}
		bind:value
		class="nx-input"
		spellcheck="false"
		onkeydown={keydown}
		oninput={() => {
			typed = true;
			dismissed = false;
			active = -1;
		}}
		onfocus={() => {
			focused = true;
			reopen();
		}}
		onclick={() => {
			if (dismissed) reopen();
		}}
		onblur={(ev) => {
			focused = false;
			onblur?.(ev);
		}}
	/>
	{#if filtered.length}
		<div class="list" bind:this={listEl}>
			{#each filtered as it, i (it.name)}
				<button
					class="item"
					class:active={i === active}
					onmousedown={(ev) => {
						ev.preventDefault();
						accept(i);
					}}
				>
					<span class="nm">{it.name}</span>
					{#if it.detail}<span class="dt">{it.detail}</span>{/if}
				</button>
			{/each}
		</div>
	{/if}
</span>

<style>
	.suggest {
		position: relative;
		display: inline-flex;
	}
	.suggest input {
		flex: 1;
		width: 100%;
	}
	.list {
		position: absolute;
		top: 100%;
		left: 0;
		min-width: 100%;
		margin-top: 2px;
		z-index: 40;
		display: flex;
		flex-direction: column;
		max-height: 200px;
		overflow-y: auto;
		background: var(--nx-panel-bg);
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		box-shadow: var(--nx-shadow);
	}
	.item {
		display: flex;
		justify-content: space-between;
		gap: 14px;
		background: transparent;
		border: none;
		color: var(--nx-ui-ink);
		padding: 3px 8px;
		cursor: pointer;
		font-family: var(--nx-mono);
		font-size: 12px;
		text-align: left;
		white-space: nowrap;
	}
	.item:hover,
	.item.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.item .dt {
		font-size: 10px;
		opacity: 0.65;
	}
</style>
