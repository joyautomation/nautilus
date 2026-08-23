<script lang="ts">
	// A run of pipe between two things on a schematic, with flow animation.
	//
	// The animation is a SECOND copy of the same path, dashed, marching along
	// it — and it is drawn only when `flowing` is true. That is the whole point
	// of the component: a still line means still water, so an operator reads
	// movement from the picture rather than from a number. `flowing` should
	// come from a meter with a deadband, never from "the pump is commanded on".
	//
	// Renders an SVG `<g>` for embedding in an `<svg>` you own. `dead` (the
	// meter is not published) draws the wire dimmed rather than inventing a
	// still-or-moving answer.
	//
	// Colours: `--flow-wire`, `--flow-color`, `--flow-hover`.
	import type { MouseEventHandler } from 'svelte/elements';

	let {
		d,
		flowing = false,
		dead = false,
		dashed = false,
		color,
		width = 1.6,
		onmousemove,
		onmouseleave,
		onclick
	}: {
		/** SVG path data for the run. */
		d: string;
		/** Something is actually moving through it — draws the marching dashes. */
		flowing?: boolean;
		/** No reading at all: the wire dims instead of claiming "still". */
		dead?: boolean;
		/** Draw the static wire dashed — the convention for a normally-closed
		 *  or intermittent connection. */
		dashed?: boolean;
		/** Flow-dash colour. Defaults to `--flow-color`. */
		color?: string;
		/** Static wire stroke width. */
		width?: number;
		onmousemove?: MouseEventHandler<SVGGElement>;
		onmouseleave?: MouseEventHandler<SVGGElement>;
		onclick?: MouseEventHandler<SVGGElement>;
	} = $props();
</script>

<g
	class="link"
	class:moving={flowing}
	class:dead
	class:dashed
	role="presentation"
	{onmousemove}
	{onmouseleave}
	{onclick}
>
	<path {d} class="wire" style:stroke-width={width} />
	{#if flowing}
		<path {d} class="dash" style:stroke={color ?? 'var(--flow-color, var(--s1))'} />
	{/if}
</g>

<style>
	.wire {
		fill: none;
		stroke: var(--flow-wire, var(--ink-2));
		opacity: 0.5;
	}

	.link.dashed .wire {
		stroke-dasharray: 5 3;
		opacity: 0.4;
	}

	.link.dead .wire {
		stroke: var(--muted);
		opacity: 0.25;
	}

	.link.moving .wire {
		opacity: 0.85;
	}

	.link:hover .wire {
		stroke: var(--flow-hover, var(--accent));
		opacity: 1;
	}

	.dash {
		fill: none;
		stroke-width: 2.6;
		stroke-dasharray: 7 9;
		animation: nautilus-flow 1.1s linear infinite;
	}

	@keyframes nautilus-flow {
		to {
			stroke-dashoffset: -16;
		}
	}

	/* Reduced motion: stop the march. The dashes stay, so the "flowing"
	   indication survives without depending on animation. */
	:global(:root[data-motion='reduced']) .dash {
		animation: none;
	}
</style>
