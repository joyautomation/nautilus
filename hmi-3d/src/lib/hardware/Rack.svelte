<script lang="ts">
	// A 19-inch rack frame from a RackLayout (rack.ts): four posts, the
	// floor frame and top, and a unit scale on the left front post. The
	// devices and cables are the caller's: place each <Server> with
	// placeDevice(). Nothing here takes a pick.
	import { T } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import { uY, type RackLayout } from './rack.js';

	let { layout, label }: { layout: RackLayout; label?: string } = $props();
	const noRaycast = () => {};
	const POST = 0.018;
	// Post centres: just outside a 19" chassis's ears (482.6 mm).
	const X = 0.2413 + POST / 2;
	let D = $derived(layout.depth / 1000);
	let top = $derived(uY(layout, layout.units + 1));
	let marks = $derived(Array.from({ length: Math.floor(layout.units / 5) }, (_, i) => (i + 1) * 5));
	const frame = { color: '#34363a', metalness: 0.5, roughness: 0.55 };
</script>

{#each [-1, 1] as sx}
	{#each [0, -D] as z}
		<T.Mesh position={[sx * X, top / 2, z]} raycast={noRaycast}>
			<T.BoxGeometry args={[POST, top, POST]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
	<!-- side rails, floor and top -->
	{#each [0.02, top] as y}
		<T.Mesh position={[sx * X, y, -D / 2]} raycast={noRaycast}>
			<T.BoxGeometry args={[POST, 0.02, D]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
{/each}
{#each [0, -D] as z}
	{#each [0.02, top] as y}
		<T.Mesh position={[0, y, z]} raycast={noRaycast}>
			<T.BoxGeometry args={[2 * X, 0.02, POST]} />
			<T.MeshStandardMaterial {...frame} />
		</T.Mesh>
	{/each}
{/each}
{#each marks as u}
	<HTML position={[-X - 0.02, uY(layout, u) + 0.022, 0]} center pointerEvents="none">
		<span class="u">U{u}</span>
	</HTML>
{/each}
{#if label}
	<HTML position={[0, top + 0.05, 0]} center pointerEvents="none">
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
