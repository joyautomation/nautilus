<script lang="ts">
	// A vertical tank: a transparent shell with the fluid level inside.
	// Reads Level / TempC off its struct; `level` / `tempC` props (from a
	// `bind`) override the members, so a flat-tag project can use it too.
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { tankState } from '../kinds.js';
	import type { NodeProps } from '../registry.js';
	import type { ViewState } from './view.js';

	let {
		value,
		good = true,
		level,
		tempC,
		radius = 0.2,
		height = 0.4
	}: NodeProps & { level?: unknown; tempC?: unknown; radius?: number; height?: number } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	// PBR values in the lit look (docs/design/spatial-hmi.md §3c); the flat
	// look keeps Milestone 1's matte. The colours never change.
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };
	let steel = $derived(view.lit ? { metalness: 0.75, roughness: 0.35 } : { metalness: 0, roughness: 0.6 });
	let dark = $derived(view.lit ? { metalness: 0.6, roughness: 0.55 } : { metalness: 0, roughness: 0.7 });

	let s = $derived(tankState(value, { level, tempC }));
	let fluidH = $derived(Math.max(0.002, (height - 0.01) * (s.level / 100)));
</script>

<T.Group>
	<!-- shell -->
	<T.Mesh position={[0, height / 2, 0]}>
		<T.CylinderGeometry args={[radius, radius, height, 48, 1, true]} />
		<T.MeshStandardMaterial
			color={palette.shell}
			transparent
			opacity={view.lit ? 0.22 : 0.18}
			side={2}
			depthWrite={false}
			metalness={0}
			roughness={view.lit ? 0.08 : 0.5}
		/>
	</T.Mesh>
	<T.Mesh position={[0, 0.0025, 0]} castShadow receiveShadow>
		<T.CylinderGeometry args={[radius, radius, 0.005, 48]} />
		<T.MeshStandardMaterial color={palette.steelDark} {...dark} />
	</T.Mesh>
	<!-- contents -->
	<T.Mesh position={[0, 0.005 + fluidH / 2, 0]}>
		<T.CylinderGeometry args={[radius - 0.01, radius - 0.01, fluidH, 48]} />
		<T.MeshStandardMaterial
			color={good ? palette.fluid : palette.stale}
			transparent
			opacity={good ? 0.85 : 0.4}
			metalness={0}
			roughness={view.lit ? 0.15 : 0.5}
		/>
	</T.Mesh>
</T.Group>
