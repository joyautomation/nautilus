<script lang="ts">
	// A pulsing box around an asset with an active alarm, coloured by its
	// worst priority. Unacknowledged pulses; acknowledged holds steady. In
	// the paper look, where nothing else has colour, it is also a soft
	// glow: a translucent fill, a little larger than the box.
	import { T, useTask } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';
	import type { ViewState } from './view.js';

	let {
		size,
		center = [0, 0, 0],
		priority,
		unacked
	}: { size: Vec3; center?: Vec3; priority: string; unacked: boolean } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };

	let t = $state(0);
	useTask((dt) => {
		if (unacked) t += dt;
	});
	let opacity = $derived(unacked ? 0.35 + 0.35 * Math.sin(t * 6) : 0.5);
	let color = $derived(palette.priority[priority] ?? palette.priority.high);
</script>

{#if view.paper}
	<!-- the glow: no cage, a drawing has no wireframes -->
	<T.Mesh position={center} raycast={() => {}} userData={{ ink: false }}>
		<T.BoxGeometry args={size.map((v) => v * 1.12) as Vec3} />
		<T.MeshBasicMaterial {color} transparent opacity={opacity * 0.45} depthWrite={false} />
	</T.Mesh>
{:else}
	<T.Mesh position={center}>
		<T.BoxGeometry args={size} />
		<T.MeshBasicMaterial {color} wireframe transparent {opacity} />
	</T.Mesh>
{/if}
