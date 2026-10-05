<script lang="ts">
	// A device's slide rails, in the rack's frame: on each side an outer
	// member bolted to the front and rear mounting rails (brackets at both
	// ends), an inner member on the device's side that travels with it, and
	// the middle member between them that travels half as far, the way a
	// telescoping rail kit extends when a device is pulled out. `slide` is
	// how far out the device is (metres, 0 = racked). Nothing takes a pick.
	import { T } from '@threlte/core';
	import { EAR_T, RAIL_X, type Placement } from './rack.js';
	import type { Vec3 } from '../scene.js';

	let {
		place,
		size,
		depth,
		slide = 0
	}: {
		/** Where the device sits racked (placeDevice). */
		place: Placement;
		/** The device, metres: width, height, depth. */
		size: Vec3;
		/** The rack, front mounting rail to rear, metres. */
		depth: number;
		slide?: number;
	} = $props();
	const noRaycast = () => {};
	const deg = Math.PI / 180;
	const TALL = 0.018;
	const T_OUT = 0.002;
	const T_MID = 0.0015;
	const T_IN = 0.0012;
	const SETBACK = EAR_T + 0.002; // behind the front mounting rail
	let W = $derived(size[0]);
	let y = $derived(size[1] / 2);
	let len = $derived(depth - 2 * SETBACK);
	let dev = $derived(Math.min(size[2], len));
	// Across, from the rail's opening in: outer, middle, inner.
	const xOut = RAIL_X - 0.001 - T_OUT / 2;
	const xMid = xOut - T_OUT / 2 - T_MID / 2;
	let xIn = $derived(Math.max(W / 2 + T_IN / 2, xMid - T_MID / 2 - T_IN / 2));
	const steel = { color: '#5f6368', metalness: 0.6, roughness: 0.5 };
</script>

<T.Group position={place.pos} rotation={[0, place.rotY * deg, 0]}>
	{#each [-1, 1] as sx}
		<!-- outer member, fixed between the rails, and its brackets -->
		<T.Mesh position={[sx * xOut, y, -SETBACK - len / 2]} raycast={noRaycast}>
			<T.BoxGeometry args={[T_OUT, TALL, len]} />
			<T.MeshStandardMaterial {...steel} />
		</T.Mesh>
		{#each [-SETBACK + 0.001, -SETBACK - len - 0.001] as z}
			<T.Mesh position={[sx * (RAIL_X + 0.004), y, z]} raycast={noRaycast}>
				<T.BoxGeometry args={[0.012, TALL * 1.6, 0.002]} />
				<T.MeshStandardMaterial {...steel} />
			</T.Mesh>
		{/each}
		<!-- middle member: half the travel -->
		<T.Mesh position={[sx * xMid, y, -SETBACK - dev / 2 + slide / 2]} raycast={noRaycast}>
			<T.BoxGeometry args={[T_MID, TALL * 0.8, dev]} />
			<T.MeshStandardMaterial {...steel} />
		</T.Mesh>
		<!-- inner member, on the device -->
		<T.Mesh position={[sx * xIn, y, -dev / 2 - 0.005 + slide]} raycast={noRaycast}>
			<T.BoxGeometry args={[T_IN, TALL * 0.6, dev - 0.01]} />
			<T.MeshStandardMaterial {...steel} />
		</T.Mesh>
	{/each}
</T.Group>
