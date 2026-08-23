<script lang="ts">
	// One node's box on a coordinate canvas. Recursive: a container node
	// renders its children through this same component.
	//
	// This owns POSITION only — where the box is, how big it is, whether it is
	// visible, and what inline style it carries. What goes IN the box is the
	// host app's business, resolved through the registry or the leaf snippet.
	// Internal to CoordinateCanvas; not exported.
	import type { Snippet } from 'svelte';
	import {
		type CanvasNode,
		type CanvasRegistry,
		inlineStyle,
		flexStyle
	} from '../canvas.js';
	import Self from './CanvasBox.svelte';

	let {
		node,
		registry,
		leaf,
		containers,
		graphics,
		stretch,
		visible,
		style: styleOf,
		href
	}: {
		node: CanvasNode;
		registry: CanvasRegistry;
		leaf?: Snippet<[CanvasNode]>;
		containers: string[];
		graphics: string[];
		stretch?: (node: CanvasNode) => boolean;
		visible?: (node: CanvasNode) => boolean;
		style?: (node: CanvasNode) => string;
		href?: (node: CanvasNode) => string | undefined;
	} = $props();

	const absolute = $derived(node.x !== undefined);
	const container = $derived(containers.includes(node.t));
	/** A component node centres its component in the box the source gave it. */
	const component = $derived(!container && !graphics.includes(node.t));

	const shown = $derived(!node.hidden && (visible?.(node) ?? true));
	const extra = $derived(styleOf?.(node) ?? '');
	const link = $derived(container ? href?.(node) : undefined);
	const Comp = $derived(registry[node.t]);
</script>

{#if shown}
	<div
		class="node"
		class:abs={absolute}
		class:box={container}
		class:comp={component}
		class:stretch={stretch?.(node) ?? false}
		style:left={absolute ? `${node.x}px` : undefined}
		style:top={absolute ? `${node.y}px` : undefined}
		style:width={absolute && node.w ? `${node.w}px` : undefined}
		style:height={absolute && node.h ? `${node.h}px` : undefined}
		style:flex-basis={!absolute && node.basis && node.basis !== 'AUTO'
			? String(node.basis)
			: undefined}
		style:flex-grow={!absolute && node.grow !== undefined ? String(node.grow) : undefined}
		style:flex-shrink={!absolute && node.shrink !== undefined ? String(node.shrink) : undefined}
		style={[inlineStyle(node), flexStyle(node), extra].filter(Boolean).join(';')}
	>
		{#if container}
			{#if link}
				<a class="navwrap" href={link} aria-label={link}></a>
			{/if}
			{#each node.c ?? [] as child, i (i)}
				<Self
					node={child}
					{registry}
					{leaf}
					{containers}
					{graphics}
					{stretch}
					{visible}
					style={styleOf}
					{href}
				/>
			{/each}
		{:else if Comp}
			<Comp {node} />
		{:else if leaf}
			{@render leaf(node)}
		{/if}
	</div>
{/if}

<style>
	.node {
		position: relative;
		box-sizing: border-box;
		min-width: 0;
		min-height: 0;
	}

	.abs {
		position: absolute;
	}

	/* A flex container lays its children out in a row unless the spec's own
	   `direction` says otherwise — the same default the source packages use.
	   A coordinate container just holds absolute children. */
	.box {
		display: flex;
	}

	/* Component boxes centre their component in the box the source gave it and
	   let a state word or caption spill, as `overflow: visible` does there. */
	.comp {
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: visible;
	}

	/* A composition that sizes to ITS box rather than centring at its intrinsic
	   size (caption over symbol over value pill). */
	.stretch {
		align-items: stretch;
	}

	.navwrap {
		position: absolute;
		inset: 0;
		z-index: 1;
	}
</style>
