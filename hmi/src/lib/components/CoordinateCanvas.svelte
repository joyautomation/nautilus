<script lang="ts">
	// A fixed coordinate plane, scaled to the viewport.
	//
	// Lay everything out in the canvas's OWN pixels, then scale the whole plane
	// once with a CSS transform. Scaling the plane rather than the boxes is what
	// lets ordinary components drop in unchanged — a 25 px pill is a 25 px pill
	// on the canvas, and its text scales with it instead of reflowing out of its
	// box. It is also why the schematic keeps its aspect ratio: a pipe stays
	// attached to the pump it feeds at every window size.
	//
	// Give it a `registry` mapping each node's `t` to a component, and/or a
	// `leaf` snippet for whatever the registry does not cover:
	//
	// ```svelte
	// <CoordinateCanvas {spec} registry={{ tank: Tank, pump: Pump }}>
	//   {#snippet leaf(node)}<Unmapped {node} />{/snippet}
	// </CoordinateCanvas>
	// ```
	import type { Snippet } from 'svelte';
	import {
		type CanvasSpec,
		type CanvasNode,
		type CanvasRegistry,
		CONTAINER_TYPES
	} from '../canvas.js';
	import CanvasBox from './CanvasBox.svelte';

	let {
		spec,
		registry = {},
		leaf,
		fit = false,
		containers = CONTAINER_TYPES,
		graphics = [],
		stretch,
		visible,
		style,
		href,
		fontFamily = 'var(--font)',
		fontSize = 'var(--font-sm)',
		color = 'var(--ink)'
	}: {
		/** The screen: its design-time canvas, and the nodes on it. */
		spec: CanvasSpec;
		/** Node type → component. Each gets `node`, inside its positioned box. */
		registry?: CanvasRegistry;
		/** Fallback renderer for node types the registry has no entry for. */
		leaf?: Snippet<[CanvasNode]>;
		/** Fit the WHOLE canvas on screen (letterboxed) rather than filling the
		 *  available width and scrolling. */
		fit?: boolean;
		/** Node types that recurse into `node.c` instead of rendering a
		 *  component. Defaults to `['coord', 'flex']`. */
		containers?: string[];
		/** Node types that FILL their box (a pipe, a line, a label, an image)
		 *  rather than centring a component inside it. */
		graphics?: string[];
		/** Return true for a node whose content should stretch to the box height
		 *  instead of centring at its intrinsic size. */
		stretch?: (node: CanvasNode) => boolean;
		/** Live visibility, for a spec whose nodes carry a `visible` binding.
		 *  `node.hidden` is honoured regardless. */
		visible?: (node: CanvasNode) => boolean;
		/** Extra inline style per node — where a live colour or background
		 *  binding lands. Appended after the node's own static style. */
		style?: (node: CanvasNode) => string;
		/** A navigable container: returns an href for a full-box overlay link. */
		href?: (node: CanvasNode) => string | undefined;
		/** Type on the canvas. The source package's own default, not the kit's,
		 *  is usually what a transcribed screen wants — the specs only override
		 *  size and weight, so everything else inherits from here. */
		fontFamily?: string;
		fontSize?: string;
		color?: string;
	} = $props();

	let frameW = $state(0);
	let frameH = $state(0);

	const scale = $derived.by(() => {
		if (!frameW) return 1;
		const byW = frameW / spec.width;
		if (!fit || !frameH) return byW;
		return Math.min(byW, frameH / spec.height);
	});
</script>

<div class="frame" bind:clientWidth={frameW} bind:clientHeight={frameH}>
	<div
		class="canvas"
		style:width={`${spec.width}px`}
		style:height={`${spec.height}px`}
		style:transform={`scale(${scale})`}
		style:margin-bottom={`${spec.height * (scale - 1)}px`}
		style:font-family={fontFamily}
		style:font-size={fontSize}
		style:color
	>
		{#each spec.items as item, i (i)}
			<CanvasBox
				node={item}
				{registry}
				{leaf}
				{containers}
				{graphics}
				{stretch}
				{visible}
				{style}
				{href}
			/>
		{/each}
	</div>
</div>

<style>
	.frame {
		width: 100%;
		overflow: hidden;
	}

	.canvas {
		position: relative;
		transform-origin: top left;
	}
</style>
