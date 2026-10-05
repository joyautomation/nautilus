<script lang="ts">
	// A 19-inch rack frame from a RackLayout (rack.ts): four posts running
	// the full height with the bottom and top frames butted square into
	// them, a mounting rail front and rear with the EIA-310 square holes
	// (three per unit) that the devices' ears bolt to, and a unit scale on
	// the left front post. The devices, their slide rails (<SlideRails>) and
	// cables are the caller's: place each <Server> with placeDevice().
	// Nothing here takes a pick.
	import { T } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import { BoxGeometry } from 'three';
	import { mergeGeometries } from 'three/examples/jsm/utils/BufferGeometryUtils.js';
	import { uY, holeYs, EAR_T, EAR_X, HOLE_X, RAIL_X, type RackLayout } from './rack.js';

	let { layout, label }: { layout: RackLayout; label?: string } = $props();
	const noRaycast = () => {};
	const POST = 0.018;
	const BEAM = 0.02;
	// The mounting rail: a flat strip from the opening's edge out past the
	// ears, just behind them; the post stands behind the rail.
	const RAIL_T = 0.002;
	const RAIL_W = EAR_X + 0.006 - RAIL_X;
	const railZ = -EAR_T - RAIL_T / 2;
	const X = RAIL_X + RAIL_W - POST / 2; // post centres, flush with the rail's outer edge
	const postZ = -EAR_T - RAIL_T - POST / 2;
	let D = $derived(layout.depth / 1000);
	// The units fill the mounting rails from `lo` to `top`; the frame's beams
	// sit outside that space, below the first unit and above the last, the way
	// a rack is built, so every unit's holes are on open rail. Below the
	// bottom beam the posts stand on the floor as feet.
	let top = $derived(uY(layout, layout.units + 1));
	let lo = $derived(Math.max(BEAM, uY(layout, 1)));
	let frameTop = $derived(top + BEAM);
	let marks = $derived(Array.from({ length: Math.floor(layout.units / 5) }, (_, i) => (i + 1) * 5));
	const frame = { color: '#34363a', metalness: 0.5, roughness: 0.55 };
	const rail = { color: '#4a4d52', metalness: 0.6, roughness: 0.45 };
	// Front posts at postZ, rear ones mirrored about the middle of the depth.
	let zs = $derived([postZ, -D - postZ]);
	// Every square hole on one rail face, one geometry (a few hundred boxes).
	const HOLE = 0.0095;
	let holes = $derived(
		mergeGeometries(
			holeYs(layout).map((y) => {
				const g = new BoxGeometry(HOLE, HOLE, 0.0004);
				g.translate(0, y, 0);
				return g;
			})
		)
	);
	$effect(() => {
		const h = holes;
		return () => h.dispose();
	});
</script>

{#each [-1, 1] as sx}
	<!-- posts, full height: the frame's beams stop at their faces -->
	{#each zs as z}
		<T.Mesh position={[sx * X, frameTop / 2, z]} raycast={noRaycast}>
			<T.BoxGeometry args={[POST, frameTop, POST]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
	<!-- side beams, just below the first unit and just above the last, between the posts -->
	{#each [lo - BEAM / 2, top + BEAM / 2] as y}
		<T.Mesh position={[sx * X, y, (zs[0] + zs[1]) / 2]} raycast={noRaycast}>
			<T.BoxGeometry args={[POST, BEAM, zs[0] - zs[1] - POST]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
	<!-- mounting rails, front and rear, with their holes -->
	{#each [railZ, -D - railZ] as z, i}
		{@const face = i === 0 ? 1 : -1}
		<T.Mesh position={[sx * (RAIL_X + RAIL_W / 2), (lo + top) / 2, z]} raycast={noRaycast}>
			<T.BoxGeometry args={[RAIL_W, top - lo, RAIL_T]} />
			<T.MeshStandardMaterial {...rail} />
		</T.Mesh>
		<T.Mesh geometry={holes} position={[sx * HOLE_X, 0, z + (face * (RAIL_T + 0.0004)) / 2]} raycast={noRaycast}>
			<T.MeshStandardMaterial color="#141516" roughness={0.9} />
		</T.Mesh>
	{/each}
{/each}
<!-- front and rear beams, just below the first unit and just above the last -->
{#each zs as z}
	{#each [lo - BEAM / 2, top + BEAM / 2] as y}
		<T.Mesh position={[0, y, z]} raycast={noRaycast}>
			<T.BoxGeometry args={[2 * X - POST, BEAM, POST]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
{/each}
{#each marks as u}
	<HTML position={[-X - 0.03, uY(layout, u) + 0.022, 0]} center pointerEvents="none">
		<span class="u">U{u}</span>
	</HTML>
{/each}
{#if label}
	<HTML position={[0, frameTop + 0.05, 0]} center pointerEvents="none">
		<span class="name">{label}</span>
	</HTML>
{/if}

<style>
	.u {
		font: 10px/1 ui-monospace, monospace;
		color: var(--ink-2, #a8a6a1);
		opacity: 0.7;
	}
	.name {
		font: 600 12px/1 system-ui, sans-serif;
		white-space: nowrap;
		color: var(--ink, #e8e6e1);
	}
</style>
