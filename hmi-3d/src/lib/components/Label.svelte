<script lang="ts">
	// A floating label: the node's title and its kind's status text, as
	// lettering on the scene. Dark ink with a tight halo in the backdrop's
	// colour — no card, no border — so the line work shows between the
	// letters instead of disappearing under a box. It is sized in metres of
	// the scene (LABEL_M), so it shrinks with the drawing on a phone and, in
	// perspective, the far labels are the smaller ones; MIN_PX keeps it
	// legible, MAX_PX keeps a close-up from shouting. Bad quality reads as
	// italic and dimmed — "stale", never a healthy-looking number.
	import { T, useTask, useThrelte } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { Vector3, type Group, type OrthographicCamera, type PerspectiveCamera } from 'three';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';
	import type { ViewState } from './view.js';

	/** The title's font size in scene metres: 12 px across a 7.5 m wide
	 * drawing on a 1400 px desktop view, 10 px (the floor) on a phone. */
	const LABEL_M = 0.065;
	const MIN_PX = 10;
	const MAX_PX = 14;

	let {
		at,
		title,
		value = '',
		good = true,
		anchor = 'center',
		onpick
	}: {
		at: Vec3;
		title: string;
		value?: string;
		good?: boolean;
		/** Which of the label's edges sits on `at`: its middle (over a node),
		 * or its right or left end, vertically centred (a tag beside a
		 * device in a rack, level with it). */
		anchor?: 'center' | 'left' | 'right';
		/** A click on the lettering picks the node it names — the one way to
		 * pick a node whose model sits behind another's. Absent: the label
		 * takes no pointer events at all. */
		onpick?: () => void;
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState | undefined>('hmi3d:view');
	const { camera, size } = useThrelte();

	// The halo is whatever the label floats over: the paper, or the theme's
	// surface in the lit and flat looks. Eight 1 px copies make a solid rim,
	// two soft ones fill the corners of the counters.
	const halo = $derived(view?.paper ? palette.paper : palette.label.bg);
	const shadow = $derived(
		['1px 0', '-1px 0', '0 1px', '0 -1px', '1px 1px', '-1px -1px', '1px -1px', '-1px 1px']
			.map((o) => `${o} 0 ${halo}`)
			.concat([`0 0 3px ${halo}`, `0 0 3px ${halo}`])
			.join(', ')
	);

	// Pixels per scene metre at the label, from the camera: an orthographic
	// camera's zoom IS that (Threlte sizes its frustum in pixels); a
	// perspective camera's follows from the vertical fov and the distance.
	let group = $state<Group>();
	let px = $state(12);
	const here = new Vector3();
	const eye = new Vector3();
	useTask(
		() => {
			const cam = camera.current;
			if (!group || !cam) return;
			let perMetre: number;
			if ((cam as OrthographicCamera).isOrthographicCamera) {
				perMetre = (cam as OrthographicCamera).zoom;
			} else {
				const p = cam as PerspectiveCamera;
				const dist = here.setFromMatrixPosition(group.matrixWorld).distanceTo(eye.setFromMatrixPosition(p.matrixWorld));
				perMetre = size.current.height / (2 * Math.tan((p.fov * Math.PI) / 360) * Math.max(dist, 1e-3));
			}
			const want = Math.min(MAX_PX, Math.max(MIN_PX, LABEL_M * perMetre));
			// A quarter pixel of hysteresis: no DOM write while the camera rests.
			if (Math.abs(want - px) >= 0.25) px = want;
		},
		{ autoInvalidate: false }
	);
</script>

<T.Group position={at} bind:ref={group}>
	<HTML center pointerEvents={onpick ? 'auto' : 'none'}>
		{#snippet text()}
			<b>{title}</b>{#if value}<span>{value}</span>{/if}
		{/snippet}
		{#if onpick}
			<!-- A real button: the lettering picks the node it names. The
			     overlay sits inside the canvas's wrapper, where the orbit
			     controls and the raycaster listen: the pointer-down stays here
			     (the orbit would capture the pointer and swallow the click),
			     and the click stays here (behind the lettering is the floor,
			     which would deselect). Capture-phase handlers: Svelte delegates
			     a plain onclick to the root, which is too late to stop a native
			     listener on the wrapper in between. -->
			<button
				type="button"
				class="lbl pick"
				class:toLeft={anchor === 'right'}
				class:toRight={anchor === 'left'}
				class:bad={!good}
				style:font-size="{px}px"
				style:color={palette.label.ink}
				style:text-shadow={shadow}
				onpointerdowncapture={(e) => e.stopPropagation()}
				onclickcapture={(e) => {
					e.stopPropagation();
					onpick();
				}}
			>
				{@render text()}
			</button>
		{:else}
			<div
				class="lbl"
				class:toLeft={anchor === 'right'}
				class:toRight={anchor === 'left'}
				class:bad={!good}
				style:font-size="{px}px"
				style:color={palette.label.ink}
				style:text-shadow={shadow}
			>
				{@render text()}
			</div>
		{/if}
	</HTML>
</T.Group>

<style>
	.lbl {
		display: flex;
		align-items: baseline;
		gap: 0.5em;
		padding: 0 2px;
		font-family: system-ui, sans-serif;
		line-height: 1.3;
		white-space: nowrap;
	}
	/* The value is the quieter half: a step smaller, a shade lighter. */
	.lbl span {
		font-size: 0.92em;
		opacity: 0.8;
	}
	/* `center` on the HTML centres the box on the point; shift it by half
	   its width so an end sits there instead */
	.toLeft {
		transform: translateX(-50%);
	}
	.toRight {
		transform: translateX(50%);
	}
	/* The button is lettering too: no chrome of its own. */
	.pick {
		cursor: pointer;
		background: none;
		border: 0;
		margin: 0;
		font: inherit;
		font-size: inherit;
	}
	.bad {
		opacity: 0.6;
		font-style: italic;
	}
</style>
