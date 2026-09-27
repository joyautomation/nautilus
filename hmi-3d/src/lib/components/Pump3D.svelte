<script lang="ts">
	// A motor-driven pump on a skid. The coupling turns at the bound speed
	// (100 % = 2 rev/s: fast enough to read as running, slow enough not to
	// strobe at 60 fps); the body goes green while running, grey otherwise.
	import { T, useTask } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { pumpState } from '../kinds.js';
	import type { NodeProps } from '../registry.js';

	let {
		value,
		good = true,
		running,
		fault,
		speed
	}: NodeProps & { running?: unknown; fault?: unknown; speed?: unknown } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	let s = $derived(pumpState(value, { running, fault, speed }));
	let angle = $state(0);
	useTask((dt) => {
		if (good && s.speed > 0) angle = (angle + (s.speed / 100) * 2 * Math.PI * 2 * dt) % (2 * Math.PI);
	});
	let body = $derived(!good ? palette.stale : s.running ? palette.running : palette.steel);
</script>

<T.Group>
	<!-- skid -->
	<T.Mesh position={[0, 0.01, 0]}>
		<T.BoxGeometry args={[0.36, 0.02, 0.14]} />
		<T.MeshStandardMaterial color={palette.steelDark} />
	</T.Mesh>
	<!-- motor -->
	<T.Mesh position={[-0.08, 0.08, 0]} rotation={[0, 0, Math.PI / 2]}>
		<T.CylinderGeometry args={[0.055, 0.055, 0.16, 32]} />
		<T.MeshStandardMaterial color={body} />
	</T.Mesh>
	<!-- coupling: the part that visibly turns -->
	<T.Group position={[0.02, 0.08, 0]} rotation={[angle, 0, 0]}>
		<T.Mesh rotation={[0, 0, Math.PI / 2]}>
			<T.CylinderGeometry args={[0.03, 0.03, 0.04, 6]} />
			<T.MeshStandardMaterial color={palette.steelDark} flatShading />
		</T.Mesh>
		<T.Mesh>
			<T.BoxGeometry args={[0.042, 0.075, 0.008]} />
			<T.MeshStandardMaterial color={palette.shell} />
		</T.Mesh>
	</T.Group>
	<!-- volute -->
	<T.Mesh position={[0.1, 0.08, 0]} rotation={[0, 0, Math.PI / 2]}>
		<T.CylinderGeometry args={[0.065, 0.065, 0.06, 32]} />
		<T.MeshStandardMaterial color={body} />
	</T.Mesh>
</T.Group>
