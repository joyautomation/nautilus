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

	// Thumbnails render each component at its real THUMB_W width (its own
	// `max-width: 100%` svg needs a sized parent — a shrink-to-fit one
	// collapses it to a sliver), then scale the result to fit the tile's
	// box, centered. Measured, not a fixed factor: a tall Tank and a wide
	// Gauge both fill the box, and a custom component (whose size is its
	// own business, and which may paint late) refits when it resizes.
	const THUMB_W = 120;
	const BOX_W = 84;
	const BOX_H = 64;
	function fit(node: HTMLElement) {
		const apply = () => {
			const w = node.offsetWidth, h = node.offsetHeight;
			if (!w || !h) return;
			const k = Math.min(BOX_W / w, BOX_H / h, 1.5);
			node.style.transform = `translate(-50%, -50%) scale(${k})`;
		};
		const ro = new ResizeObserver(apply);
		ro.observe(node);
		apply();
		return { destroy: () => ro.disconnect() };
	}

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
			<span class="thumb"><span class="inner" use:fit><C width={THUMB_W} {...DEMO_PROPS[name] ?? {}} /></span></span>
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
					><span class="inner" use:fit><UserIsland {name} props={{ width: THUMB_W }} /></span></span
				>
				<span class="label">{name}</span>
			</button>
		{/each}
	{/if}
</aside>

<style>
	aside {
		flex: none;
		width: 118px; /* 84 thumb + 8 item + 16 padding, + the stable scrollbar gutter */
		overflow-y: auto;
		/* the column keeps its width when the scrollbar comes and goes (arming
		   + Pipe grows the list), so the canvas beside it never shifts */
		scrollbar-gutter: stable;
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
		height: 64px;
		overflow: hidden;
		display: block;
		position: relative;
		pointer-events: none;
	}
	.inner {
		position: absolute;
		top: 50%;
		left: 50%;
		width: 120px; /* THUMB_W */
		transform: translate(-50%, -50%) scale(0.5); /* until fit() measures */
		transform-origin: center;
		display: block;
		line-height: 0;
	}
	.label {
		font-size: 11px;
		color: var(--nx-muted);
	}
</style>
