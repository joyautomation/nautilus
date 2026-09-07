<script lang="ts">
	// Sidebar navigation: sections of icon+label links, active-route
	// highlighting, collapsible to an icon-only rail. Framework-agnostic like
	// the rest of the kit — pass the current path yourself (e.g.
	// `current={page.url.pathname}` from SvelteKit's $app/state) rather than
	// this component reaching into a router directly.
	import Icon from './Icon.svelte';
	import type { NavSection } from '../types.js';

	let {
		sections,
		current = '',
		collapsed = $bindable(false),
		collapsible = true,
		brand
	}: {
		sections: NavSection[];
		/** Current route path, e.g. from `$page.url.pathname`. */
		current?: string;
		collapsed?: boolean;
		collapsible?: boolean;
		/** Optional snippet rendered above the sections, e.g. a logo/product name. */
		brand?: import('svelte').Snippet;
	} = $props();

	function isActive(href: string): boolean {
		if (!current) return false;
		if (current === href) return true;
		return href !== '/' && current.startsWith(href + '/');
	}
</script>

<nav class="nav" class:collapsed aria-label="Primary">
	{#if brand}<div class="brand">{@render brand()}</div>{/if}
	<div class="sections">
		{#each sections as section, si (section.label ?? si)}
			<div class="section">
				{#if section.label && !collapsed}<div class="section-label">{section.label}</div>{/if}
				<ul>
					{#each section.items as item (item.href)}
						<li>
							<a
								href={item.href}
								class="item"
								class:active={isActive(item.href)}
								class:disabled={item.disabled}
								aria-disabled={item.disabled}
								aria-current={isActive(item.href) ? 'page' : undefined}
								title={collapsed ? item.label : undefined}
								onclick={(e) => item.disabled && e.preventDefault()}
							>
								{#if item.icon}<Icon name={item.icon} size={18} />{/if}
								<span class="lbl">{item.label}</span>
								{#if item.badge !== undefined && !collapsed}<span class="badge">{item.badge}</span>{/if}
							</a>
						</li>
					{/each}
				</ul>
			</div>
		{/each}
	</div>
	{#if collapsible}
		<button
			type="button"
			class="collapse-toggle"
			onclick={() => (collapsed = !collapsed)}
			aria-label={collapsed ? 'Expand navigation' : 'Collapse navigation'}
		>
			<Icon name={collapsed ? 'chevron-right' : 'chevron-left'} size={16} />
			{#if !collapsed}<span>Collapse</span>{/if}
		</button>
	{/if}
</nav>

<style>
	.nav {
		display: flex;
		flex-direction: column;
		width: 220px;
		flex: none;
		height: 100%;
		background: var(--surface);
		border-right: 1px solid var(--border);
		overflow-x: hidden;
		overflow-y: auto;
		transition: width 0.15s ease;
	}
	.nav.collapsed {
		width: 56px;
	}
	.brand {
		flex: none;
		padding: 12px 14px;
		border-bottom: 1px solid var(--border);
	}
	.sections {
		flex: 1;
		padding: 10px 8px;
		display: grid;
		gap: 14px;
		align-content: start;
	}
	.section-label {
		padding: 0 8px;
		margin-bottom: 4px;
		font-size: var(--font-2xs);
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--muted);
		white-space: nowrap;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 1px;
	}
	.item {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px;
		border-radius: 7px;
		color: var(--ink-2);
		text-decoration: none;
		font-size: var(--font-xs);
		font-weight: 550;
		white-space: nowrap;
	}
	.item :global(svg) {
		flex: none;
	}
	.item .lbl {
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.collapsed .item {
		width: 40px;
		justify-content: center;
	}
	.item:hover:not(.disabled) {
		background: var(--hover);
		color: var(--ink);
	}
	.item.active {
		background: var(--surface-2);
		color: var(--s1);
	}
	.item.active :global(svg) {
		color: var(--s1);
	}
	.item.disabled {
		opacity: 0.4;
		cursor: default;
	}
	.badge {
		margin-left: auto;
		flex: none;
		padding: 1px 7px;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--ink-2);
		font-size: var(--font-2xs);
		font-weight: 650;
	}
	.item.active .badge {
		background: color-mix(in srgb, var(--s1) 20%, var(--surface-2));
		color: var(--s1);
	}
	.collapse-toggle {
		flex: none;
		display: flex;
		align-items: center;
		gap: 8px;
		margin: 8px;
		padding: 8px;
		border: 1px solid var(--border);
		border-radius: 7px;
		background: transparent;
		color: var(--muted);
		font: inherit;
		font-size: var(--font-2xs);
		cursor: pointer;
	}
	.collapsed .collapse-toggle {
		justify-content: center;
	}
	.collapse-toggle:hover {
		background: var(--hover);
		color: var(--ink-2);
	}
	.collapse-toggle:focus-visible,
	.item:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -1px;
	}

	/* Coarse pointer (touch/pen): grow nav items to the minimum hit area.
	   Desktop density (mouse/trackpad) is untouched. */
	@media (pointer: coarse) {
		.item {
			min-height: var(--tap);
		}
	}
</style>
