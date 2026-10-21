<script lang="ts">
	// The kinds a node can be: the kit's built-ins and the document's own
	// `kinds` entries (sceneKinds.ts's paletteKinds, posted by the host).
	// Click one to arm placement, then click the ground to drop it there;
	// click it again (or Esc) to disarm.
	import { armPlace, ed, openKind, type PaletteKind } from './sceneState.svelte';

	function caption(k: PaletteKind): string {
		switch (k.definedBy) {
			case 'builtin':
				return 'built-in';
			case 'model':
				return k.file ? 'glTF · file' : 'glTF';
			case 'component':
				return 'component';
			case 'assembly':
				return `assembly · ${k.parts ?? 0} part${k.parts === 1 ? '' : 's'}`;
			default:
				return 'declared';
		}
	}
	function tip(k: PaletteKind): string {
		const bits = [`Place a ${k.name}`];
		if (k.type) bits.push(`binds a ${k.type}`);
		if (k.members.length) bits.push(`reads ${k.members.join(', ')}`);
		if (k.source) bits.push(k.source);
		if (k.file) bits.push(k.file);
		return bits.join(' · ');
	}
</script>

<aside aria-label="Kinds">
	<div class="head">Kinds</div>
	{#each ed.kinds as k (k.name)}
		<button
			class="item"
			data-id={k.name}
			data-kind="palette-item"
			class:armed={ed.tool === 'place' && ed.placeKind === k.name}
			onclick={() => armPlace(k.name)}
			title={tip(k)}
		>
			<span class="name">{k.name}</span>
			<span class="cap">{caption(k)}</span>
		</button>
		{#if k.file}
			<button class="edit" data-kind="palette-edit" data-id={k.name} onclick={() => openKind(k.file!)} title="Open {k.file} in the kind editor, beside the scene">edit kind</button>
		{/if}
	{/each}
	{#if ed.kinds.length === 0}
		<div class="empty">No kinds yet</div>
	{/if}
</aside>

<style>
	aside {
		width: 150px;
		flex: none;
		overflow-y: auto;
		border-right: 1px solid var(--nx-border);
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.head {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--nx-muted);
		padding: 2px 4px 4px;
	}
	.item {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 1px;
		padding: 6px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		text-align: left;
	}
	.item:hover {
		background: var(--nx-hover);
	}
	.item.armed {
		border-color: var(--nx-accent);
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.name {
		font-weight: 600;
		font-family: var(--nx-mono);
		font-size: 12px;
	}
	.cap {
		font-size: 11px;
		color: var(--nx-muted);
	}
	.item.armed .cap {
		color: inherit;
		opacity: 0.8;
	}
	.empty {
		color: var(--nx-muted);
		font-size: 12px;
		padding: 4px;
	}
	.edit {
		align-self: flex-end;
		margin-top: -4px;
		border: 0;
		background: none;
		color: var(--nx-accent);
		cursor: pointer;
		font: inherit;
		font-size: 11px;
		padding: 0 4px 2px;
	}
	.edit:hover {
		text-decoration: underline;
	}
</style>
