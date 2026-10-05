<script lang="ts">
	// A box over the 3D. On a wide screen it is the box; on a phone it is a
	// chip (`chip`) that opens the box as a sheet above the chip row, one
	// sheet at a time (sheets.svelte.ts). Its place is the parent's.
	import type { Snippet } from 'svelte';
	import { sheets } from './sheets.svelte';

	let {
		id,
		label,
		chip,
		children,
		flush = false,
		fixed = false
	}: {
		id: string;
		label: string;
		chip: Snippet;
		children: Snippet;
		/** No padding: the content lays itself out (and scrolls its own list). */
		flush?: boolean;
		/** Never squeezed by a box below it in the same column. */
		fixed?: boolean;
	} = $props();
	let open = $derived(sheets.open === id);
</script>

{#if sheets.phone}
	<button class="chip" class:on={open} aria-expanded={open} aria-label={label} onclick={() => sheets.toggle(id)}>{@render chip()}</button>
	{#if open}
		<div class="box sheet" class:flush role="dialog" aria-label={label}>{@render children()}</div>
	{/if}
{:else}
	<div class="box" class:flush class:fixed>{@render children()}</div>
{/if}

<style>
	.box {
		pointer-events: auto;
		padding: 8px 10px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.3 system-ui, sans-serif;
		min-height: 0;
		max-height: 100%;
		flex: 0 1 auto;
		overflow: auto;
		box-sizing: border-box;
	}
	.fixed {
		flex-shrink: 0;
	}
	.flush {
		padding: 0;
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	/* above the chip row, the width of the screen */
	.sheet {
		position: fixed;
		left: 12px;
		right: 12px;
		bottom: 148px;
		max-height: calc(100vh - 148px - 150px);
		z-index: 6;
		background: var(--surface, #1a1a19);
		box-shadow: 0 6px 24px rgb(0 0 0 / 0.35);
		overflow: auto;
	}
	.sheet.flush {
		overflow: auto;
		display: block;
	}
	.chip {
		pointer-events: auto;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 6px 10px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 92%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.2 system-ui, sans-serif;
		white-space: nowrap;
		cursor: pointer;
	}
	.chip.on {
		border-color: var(--accent, #6aa5e8);
	}
</style>
