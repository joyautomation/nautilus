<script lang="ts">
	// Standalone dropdown menu: a trigger button that opens a floating list of
	// actions. Shares its item shape and outside-click/keyboard-nav machinery
	// with MenuBar (see ../menu.ts) — use this one for a single "⋯" / "Actions"
	// button; use MenuBar for a File/Edit-style app menu row.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';
	import { outsideClick, nextEnabledIndex, type MenuItem } from '../menu.js';

	let {
		items,
		label = 'Menu',
		icon = 'ellipsis-vertical',
		align = 'start',
		trigger
	}: {
		items: MenuItem[];
		/** Accessible name for the default icon-button trigger. Ignored if `trigger` is given. */
		label?: string;
		/** Icon for the default trigger. */
		icon?: string;
		/** Which edge of the trigger the panel aligns to. */
		align?: 'start' | 'end';
		/** Custom trigger content, e.g. a labeled button — receives nothing, just renders in place of the default icon button. */
		trigger?: Snippet;
	} = $props();

	let open = $state(false);
	let activeIndex = $state(-1);
	let triggerEl = $state<HTMLElement | null>(null);

	function toggle() {
		open = !open;
		activeIndex = open ? nextEnabledIndex(items, -1, 1) : -1;
	}
	function close(focusTrigger = false) {
		open = false;
		activeIndex = -1;
		if (focusTrigger) triggerEl?.focus();
	}
	function select(item: MenuItem) {
		if (item.disabled || item.separator) return;
		item.onSelect?.();
		close();
	}
	function onkeydown(ev: KeyboardEvent) {
		if (!open) {
			if (ev.key === 'Enter' || ev.key === ' ' || ev.key === 'ArrowDown') {
				ev.preventDefault();
				open = true;
				activeIndex = nextEnabledIndex(items, -1, 1);
			}
			return;
		}
		switch (ev.key) {
			case 'ArrowDown':
				ev.preventDefault();
				activeIndex = nextEnabledIndex(items, activeIndex, 1);
				break;
			case 'ArrowUp':
				ev.preventDefault();
				activeIndex = nextEnabledIndex(items, activeIndex, -1);
				break;
			case 'Home':
				ev.preventDefault();
				activeIndex = nextEnabledIndex(items, -1, 1);
				break;
			case 'End':
				ev.preventDefault();
				activeIndex = nextEnabledIndex(items, 0, -1);
				break;
			case 'Enter':
			case ' ':
				ev.preventDefault();
				if (activeIndex >= 0) select(items[activeIndex]);
				break;
			case 'Escape':
				ev.preventDefault();
				close(true);
				break;
			case 'Tab':
				close();
				break;
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="dropdown" use:outsideClick={() => close()} {onkeydown}>
	<button
		type="button"
		bind:this={triggerEl}
		class="trigger"
		class:plain={!!trigger}
		aria-haspopup="true"
		aria-expanded={open}
		{...!trigger ? { 'aria-label': label } : {}}
		onclick={toggle}
	>
		{#if trigger}
			{@render trigger()}
		{:else}
			<Icon name={icon} size={16} />
		{/if}
	</button>
	{#if open}
		<div class="panel {align}" role="menu">
			{#each items as item, i (item.label + i)}
				{#if item.separator}
					<div class="sep" role="separator"></div>
				{:else}
					{#if item.href}
						<a
							role="menuitem"
							href={item.href}
							class="item"
							class:active={activeIndex === i}
							class:disabled={item.disabled}
							aria-disabled={item.disabled}
							onclick={(e) => {
								if (item.disabled) e.preventDefault();
								else select(item);
							}}
						>
							{#if item.icon}<Icon name={item.icon} size={15} />{/if}
							<span class="lbl">{item.label}</span>
							{#if item.shortcut}<span class="shortcut">{item.shortcut}</span>{/if}
						</a>
					{:else}
						<button
							type="button"
							role="menuitem"
							class="item"
							class:active={activeIndex === i}
							disabled={item.disabled}
							onclick={() => select(item)}
						>
							{#if item.icon}<Icon name={item.icon} size={15} />{/if}
							<span class="lbl">{item.label}</span>
							{#if item.shortcut}<span class="shortcut">{item.shortcut}</span>{/if}
						</button>
					{/if}
				{/if}
			{/each}
		</div>
	{/if}
</div>

<style>
	.dropdown {
		position: relative;
		display: inline-flex;
	}
	.trigger {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 30px;
		height: 30px;
		padding: 0;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--surface-2);
		color: var(--ink-2);
		cursor: pointer;
	}
	.trigger:hover {
		background: var(--hover);
		color: var(--ink);
	}
	.trigger.plain {
		width: auto;
		height: auto;
		border: none;
		background: transparent;
		padding: 0;
	}
	.trigger:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.panel {
		position: absolute;
		top: calc(100% + 4px);
		z-index: 200;
		min-width: 180px;
		max-width: 320px;
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 4px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		box-shadow: 0 10px 28px rgba(0, 0, 0, 0.28);
	}
	.panel.start {
		left: 0;
	}
	.panel.end {
		right: 0;
	}
	.sep {
		height: 1px;
		margin: 4px 2px;
		background: var(--border);
	}
	.item {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 7px 9px;
		border: none;
		border-radius: 6px;
		background: transparent;
		color: var(--ink);
		font: inherit;
		font-size: var(--font-xs);
		text-align: left;
		text-decoration: none;
		cursor: pointer;
		white-space: nowrap;
	}
	.item :global(svg) {
		color: var(--muted);
		flex: none;
	}
	.item .lbl {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.item .shortcut {
		flex: none;
		font-size: var(--font-2xs);
		color: var(--muted);
		font-family: var(--mono);
	}
	.item:hover:not(.disabled):not(:disabled),
	.item.active {
		background: var(--hover);
	}
	.item.disabled,
	.item:disabled {
		opacity: 0.4;
		cursor: default;
	}

	/* Coarse pointer (touch/pen): grow to the minimum hit area. Desktop
	   density (mouse/trackpad) is untouched. `.trigger.plain` renders
	   caller-supplied content and is left to size itself. */
	@media (pointer: coarse) {
		.trigger:not(.plain) {
			min-width: var(--tap);
			min-height: var(--tap);
		}
		.item {
			min-height: var(--tap);
		}
	}
</style>
