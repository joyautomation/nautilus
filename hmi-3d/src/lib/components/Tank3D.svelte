<script lang="ts">
	// A vertical tank: a transparent shell with the fluid level inside.
	// Reads Level / TempC off its struct; `level` / `tempC` props (from a
	// `bind`) override the members, so a flat-tag project can use it too.
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { tankState } from '../kinds.js';
	import type { NodeProps } from '../registry.js';

	let {
		value,
		good = true,
		level,
		tempC,
		radius = 0.2,
		height = 0.4
	}: NodeProps & { level?: unknown; tempC?: unknown; radius?: number; height?: number } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	let s = $derived(tankState(value, { level, tempC }));
	let fluidH = $derived(Math.max(0.002, (height - 0.01) * (s.level / 100)));
</script>

<T.Group>
	<!-- shell -->
	<T.Mesh position={[0, height / 2, 0]}>
		<T.CylinderGeometry args={[radius, radius, height, 48, 1, true]} />
		<T.MeshStandardMaterial color={palette.shell} transparent opacity={0.18} side={2} depthWrite={false} />
	</T.Mesh>
	<T.Mesh position={[0, 0.0025, 0]}>
		<T.CylinderGeometry args={[radius, radius, 0.005, 48]} />
		<T.MeshStandardMaterial color={palette.steelDark} />
	</T.Mesh>
	<!-- contents -->
	<T.Mesh position={[0, 0.005 + fluidH / 2, 0]}>
		<T.CylinderGeometry args={[radius - 0.01, radius - 0.01, fluidH, 48]} />
		<T.MeshStandardMaterial color={good ? palette.fluid : palette.stale} transparent opacity={good ? 0.85 : 0.4} />
	</T.Mesh>
</T.Group>
