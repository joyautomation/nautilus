<script lang="ts">
	// A floating label: the node's title and its kind's status text, as an
	// HTML overlay anchored to a point in the scene. Bad quality reads as
	// italic and dimmed — "stale", never a healthy-looking number.
	import { HTML } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';

	let {
		at,
		title,
		value = '',
		good = true,
		anchor = 'center'
	}: {
		at: Vec3;
		title: string;
		value?: string;
		good?: boolean;
		/** Which of the label's edges sits on `at`: its middle (over a node),
		 * or its right or left end, vertically centred (a tag beside a
		 * device in a rack, level with it). */
		anchor?: 'center' | 'left' | 'right';
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
</script>

<HTML position={at} center pointerEvents="none">
	<div
		class="lbl"
		class:toLeft={anchor === 'right'}
		class:toRight={anchor === 'left'}
		class:bad={!good}
		style:background="color-mix(in srgb, {palette.label.bg} 85%, transparent)"
		style:color={palette.label.ink}
		style:border-color={palette.label.border}
	>
		<b>{title}</b>{#if value}<span>{value}</span>{/if}
	</div>
</HTML>

<style>
	.lbl {
		display: flex;
		gap: 6px;
		padding: 2px 6px;
		border-radius: 4px;
		font: 12px/1.3 system-ui, sans-serif;
		white-space: nowrap;
		border: 1px solid;
	}
	/* `center` on the HTML centres the box on the point; shift it by half
	   its width so an end sits there instead */
	.toLeft {
		transform: translateX(-50%);
	}
	.toRight {
		transform: translateX(50%);
	}
	.bad {
		opacity: 0.6;
		font-style: italic;
	}
</style>
