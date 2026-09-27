<script lang="ts">
	// A pulsing box around an asset with an active alarm, coloured by its
	// worst priority. Unacknowledged pulses; acknowledged holds steady.
	import { T, useTask } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';

	let {
		size,
		center = [0, 0, 0],
		priority,
		unacked
	}: { size: Vec3; center?: Vec3; priority: string; unacked: boolean } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	let t = $state(0);
	useTask((dt) => {
		if (unacked) t += dt;
	});
	let opacity = $derived(unacked ? 0.35 + 0.35 * Math.sin(t * 6) : 0.5);
	let color = $derived(palette.priority[priority] ?? palette.priority.high);
</script>

<T.Mesh position={center}>
	<T.BoxGeometry args={size} />
	<T.MeshBasicMaterial {color} wireframe transparent {opacity} />
</T.Mesh>
