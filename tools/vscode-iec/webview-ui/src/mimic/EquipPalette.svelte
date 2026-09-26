<script lang="ts">
	// Equipment palette: live thumbnails of the kit's built-ins, plus (below a
	// divider) this project's own custom components — anything with a
	// *.component.json sidecar, already placed somewhere in the open doc, or
	// just a bare `{Name}.svelte` discovered anywhere in the workspace with
	// neither yet (ed.customComponents, posted by the host's mimicManifest
	// message; see mimicEditor.ts's paletteCustomComponents). A bare one
	// lists with no ports until "Edit Component Ports…" creates its sidecar
	// on first save — dropping it here never creates one. Click one to arm placement,
	// then click the canvas to drop it — same addEquipment op path either
	// way, so a custom component's default size is whatever ITS OWN Svelte
	// component defaults to (no width override), exactly like a built-in
	// placed without an explicit width.
	import { DEMO_PROPS, registry, registryNames } from './registry';
	import { ed } from './mimicState.svelte';
	import UserIsland from './UserIsland.svelte';

	function arm(name: string) {
		if (ed.tool === 'place' && ed.placeComponent === name) {
			ed.tool = 'select';
			ed.placeComponent = '';
			return;
		}
		ed.tool = 'place';
		ed.placeComponent = name;
		ed.portsEdit = null;
	}
</script>

<aside aria-label="Equipment palette">
	{#each registryNames as name (name)}
		{@const C = registry[name]}
		<button
			class="item"
			class:armed={ed.tool === 'place' && ed.placeComponent === name}
			onclick={() => arm(name)}
			title="Place a {name}"
		>
			<span class="thumb"><span class="inner"><C width={120} {...DEMO_PROPS[name] ?? {}} /></span></span>
			<span class="label">{name}</span>
		</button>
	{/each}

	{#if ed.customComponents.length}
		<div class="divider" role="separator" aria-label="Custom components"></div>
		{#each ed.customComponents as name (name)}
			<button
				class="item"
				class:armed={ed.tool === 'place' && ed.placeComponent === name}
				onclick={() => arm(name)}
				title="Place a {name}"
			>
				<span class="thumb"
					><span class="inner"><UserIsland {name} props={{ width: 120 }} /></span></span
				>
				<span class="label">{name}</span>
			</button>
		{/each}
	{/if}
</aside>

<style>
	aside {
		flex: none;
		width: 108px;
		overflow-y: auto;
		border-right: 1px solid var(--nx-border);
		padding: 8px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.item {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		padding: 6px 4px;
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
	}
	.item:hover {
		background: var(--nx-hover);
	}
	.item.armed {
		border-color: var(--nx-accent);
		outline: 1px solid var(--nx-accent);
	}
	.divider {
		border-top: 1px solid var(--nx-border);
		margin: 2px 0;
	}
	.thumb {
		width: 84px;
		height: 56px;
		overflow: hidden;
		display: block;
		position: relative;
		pointer-events: none;
	}
	.inner {
		position: absolute;
		top: 0;
		left: 50%;
		transform: translateX(-50%) scale(0.62);
		transform-origin: top center;
		display: block;
	}
	.label {
		font-size: 11px;
		color: var(--nx-muted);
	}
</style>
