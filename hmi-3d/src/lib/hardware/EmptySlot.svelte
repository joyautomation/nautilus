<script lang="ts">
	// An empty position: a faint outline of where a part would sit, plus the
	// filler a real chassis has there (a blank bezel in a drive bay, the
	// socket of an unpopulated DIMM slot). Absent and unpublished positions
	// draw the outline in their colour, so a pulled drive is a red box.
	import { T } from '@threlte/core';
	import { BoxGeometry } from 'three';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';

	let { size, wire, filler }: { size: Vec3; wire?: string; filler?: { size: Vec3; pos: Vec3 } } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
</script>

<!-- Outlines take no clicks: three picks a line within 1 m of the ray. -->
<T.LineSegments raycast={() => {}}>
	<T.EdgesGeometry args={[new BoxGeometry(...size)]} />
	<T.LineBasicMaterial color={wire ?? palette.steelDark} transparent opacity={wire ? 0.95 : 0.35} />
</T.LineSegments>
<!-- An invisible box, so the empty position still takes a click. -->
<T.Mesh>
	<T.BoxGeometry args={size} />
	<T.MeshBasicMaterial visible={false} />
</T.Mesh>
{#if filler}
	<T.Mesh position={filler.pos}>
		<T.BoxGeometry args={filler.size} />
		<T.MeshStandardMaterial color="#1b1c1e" roughness={0.7} />
	</T.Mesh>
{/if}
