<script lang="ts">
	// Horizontal app menu bar (File/Edit-style). Data-driven: an array of
	// top-level menus, each a label + MenuItem list (shared with DropdownMenu,
	// see ../menu.ts). Click a menu to open it; while one is open, hovering or
	// arrowing to another top-level menu switches directly (standard desktop
	// menu-bar behavior). Arrow keys navigate items, Enter/Space selects,
	// Escape closes and returns focus to the trigger, outside click closes.
	import Icon from './Icon.svelte';
	import { outsideClick, nextEnabledIndex, type MenuBarMenu } from '../menu.js';

	let {
		menus,
		leading
	}: {
		menus: MenuBarMenu[];
		/** Optional snippet rendered before the menus, e.g. a brand mark. */
		leading?: import('svelte').Snippet;
	} = $props();

	let openMenu = $state<number | null>(null);
	let activeItem = $state(-1);
	let triggerEls: (HTMLElement | null)[] = [];

	function openAt(mi: number) {
		if (menus[mi].disabled) return;
		openMenu = mi;
		activeItem = nextEnabledIndex(menus[mi].items, -1, 1);
	}
	function toggle(mi: number) {
		if (openMenu === mi) close();
		else openAt(mi);
	}
	function close(focusTrigger = false) {
		const was = openMenu;
		openMenu = null;
		activeItem = -1;
		if (focusTrigger && was !== null) triggerEls[was]?.focus();
	}
	function select(item: { disabled?: boolean; separator?: boolean; onSelect?: () => void }) {
		if (item.disabled || item.separator) return;
		item.onSelect?.();
		close();
	}
	function onMenuKeydown(ev: KeyboardEvent, mi: number) {
		const items = menus[mi].items;
		switch (ev.key) {
			case 'ArrowDown':
				ev.preventDefault();
				if (openMenu === mi) activeItem = nextEnabledIndex(items, activeItem, 1);
				else openAt(mi);
				break;
			case 'ArrowUp':
				ev.preventDefault();
				if (openMenu === mi) activeItem = nextEnabledIndex(items, activeItem, -1);
				break;
			case 'ArrowRight': {
				ev.preventDefault();
				const next = (mi + 1) % menus.length;
				if (openMenu !== null) openAt(next);
				triggerEls[next]?.focus();
				break;
			}
			case 'ArrowLeft': {
				ev.preventDefault();
				const prev = (mi - 1 + menus.length) % menus.length;
				if (openMenu !== null) openAt(prev);
				triggerEls[prev]?.focus();
				break;
			}
			case 'Enter':
			case ' ':
				ev.preventDefault();
				if (openMenu === mi && activeItem >= 0) select(items[activeItem]);
				else openAt(mi);
				break;
			case 'Escape':
				ev.preventDefault();
				close(true);
				break;
			case 'Home':
				if (openMenu === mi) {
					ev.preventDefault();
					activeItem = nextEnabledIndex(items, -1, 1);
				}
				break;
			case 'End':
				if (openMenu === mi) {
					ev.preventDefault();
					activeItem = nextEnabledIndex(items, 0, -1);
				}
				break;
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="menubar" role="menubar" aria-label="Application menu" use:outsideClick={() => close()}>
	{#if leading}<div class="leading">{@render leading()}</div>{/if}
	{#each menus as menu, mi (menu.label)}
		<div class="menu-wrap">
			<button
				type="button"
				bind:this={triggerEls[mi]}
				role="menuitem"
				class="trigger"
				class:open={openMenu === mi}
				aria-haspopup="true"
				aria-expanded={openMenu === mi}
				disabled={menu.disabled}
				onclick={() => toggle(mi)}
				onmouseenter={() => {
					if (openMenu !== null && openMenu !== mi) openAt(mi);
				}}
				onkeydown={(e) => onMenuKeydown(e, mi)}
			>
				{menu.label}
			</button>
			{#if openMenu === mi}
				<div class="panel" role="menu">
					{#each menu.items as item, ii (item.label + ii)}
						{#if item.separator}
							<div class="sep" role="separator"></div>
						{:else if item.href}
							<a
								role="menuitem"
								href={item.href}
								class="item"
								class:active={activeItem === ii}
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
								class:active={activeItem === ii}
								disabled={item.disabled}
								onclick={() => select(item)}
							>
								{#if item.icon}<Icon name={item.icon} size={15} />{/if}
								<span class="lbl">{item.label}</span>
								{#if item.shortcut}<span class="shortcut">{item.shortcut}</span>{/if}
							</button>
						{/if}
					{/each}
				</div>
			{/if}
		</div>
	{/each}
</div>

<style>
	.menubar {
		display: flex;
		align-items: center;
		gap: 2px;
		background: var(--surface);
		border-bottom: 1px solid var(--border);
		padding: 4px 6px;
	}
	.leading {
		display: flex;
		align-items: center;
		margin-right: 8px;
	}
	.menu-wrap {
		position: relative;
	}
	.trigger {
		border: 1px solid transparent;
		background: transparent;
		color: var(--ink-2);
		padding: 6px 11px;
		font-size: var(--font-xs);
		font-weight: 550;
		border-radius: 6px;
		cursor: pointer;
	}
	.trigger:hover {
		background: var(--hover);
		color: var(--ink);
	}
	.trigger.open {
		background: var(--surface-2);
		color: var(--ink);
	}
	.trigger:disabled {
		opacity: 0.4;
		cursor: default;
	}
	.trigger:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -1px;
	}
	.panel {
		position: absolute;
		top: calc(100% + 2px);
		left: 0;
		z-index: 200;
		min-width: 190px;
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
	   density (mouse/trackpad) is untouched. */
	@media (pointer: coarse) {
		.trigger {
			min-height: var(--tap);
		}
		.item {
			min-height: var(--tap);
		}
	}
</style>
