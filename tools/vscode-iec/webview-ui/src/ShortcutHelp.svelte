<script lang="ts">
	// The "?" button: a small popover listing one editor's gestures and keys,
	// rendered from its table in shortcuts.ts (the same table the toolbar
	// hint line comes from).
	import type { ShortcutGroup } from './shortcuts';

	let { groups, label = 'Keyboard shortcuts and gestures' }: { groups: ShortcutGroup[]; label?: string } = $props();

	let open = $state(false);
	let root = $state<HTMLSpanElement | undefined>();
	const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
	const keys = (k: string) => (mac ? k.replace(/\bCtrl\b/g, '⌘') : k);

	function onWindowPointer(ev: PointerEvent) {
		if (open && root && !root.contains(ev.target as Node)) open = false;
	}
	function onWindowKey(ev: KeyboardEvent) {
		if (open && ev.key === 'Escape') {
			open = false;
			ev.stopPropagation();
		}
	}
</script>

<svelte:window onpointerdowncapture={onWindowPointer} onkeydowncapture={onWindowKey} />

<span class="nx-help" bind:this={root}>
	<button
		class="nx-help-btn"
		class:on={open}
		title={label}
		aria-label={label}
		aria-expanded={open}
		onclick={(e) => {
			e.stopPropagation();
			open = !open;
		}}>?</button
	>
	{#if open}
		<div class="nx-help-pop" role="dialog" aria-label={label}>
			{#each groups as g (g.title)}
				<div class="nx-help-group">{g.title}</div>
				<table>
					<tbody>
						{#each g.rows as r (r.keys + r.does)}
							<tr><th>{keys(r.keys)}</th><td>{r.does}</td></tr>
						{/each}
					</tbody>
				</table>
			{/each}
		</div>
	{/if}
</span>

<style>
	.nx-help {
		position: relative;
		flex-shrink: 0;
		display: inline-flex;
	}
	.nx-help-btn {
		width: 22px;
		height: 22px;
		padding: 0;
		border-radius: 999px;
		border: 1px solid var(--nx-border);
		background: transparent;
		color: var(--nx-ui-ink);
		font: inherit;
		font-size: 12px;
		font-weight: 600;
		line-height: 1;
		cursor: pointer;
	}
	.nx-help-btn:hover,
	.nx-help-btn.on {
		background: var(--nx-hover);
	}
	.nx-help-pop {
		position: absolute;
		top: calc(100% + 6px);
		right: 0;
		z-index: 50;
		width: max-content;
		max-width: min(440px, calc(100vw - 24px));
		max-height: 70vh;
		overflow-y: auto;
		padding: 6px 10px 8px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		border: 1px solid var(--nx-border);
		border-radius: 4px;
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.25);
		font-size: 12px;
		text-align: left;
		user-select: text;
	}
	.nx-help-group {
		margin: 6px 0 2px;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--nx-muted);
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	th {
		padding: 2px 12px 2px 0;
		font-weight: 600;
		font-family: var(--nx-mono);
		font-size: 11px;
		white-space: nowrap;
		vertical-align: top;
		text-align: left;
	}
	td {
		padding: 2px 0;
		vertical-align: top;
		white-space: normal;
	}
</style>
