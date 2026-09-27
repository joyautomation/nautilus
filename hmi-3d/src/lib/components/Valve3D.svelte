<script lang="ts">
	// A quarter-turn valve on a vertical run. The handle lies across the
	// pipe when closed and along it when open, following Pos.
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { valveState } from '../kinds.js';
	import type { NodeProps } from '../registry.js';
	import type { ViewState } from './view.js';

	let { value, good = true, pos, cmd }: NodeProps & { pos?: unknown; cmd?: unknown } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	// PBR values in the lit look (docs/design/spatial-hmi.md §3c); the flat
	// look keeps Milestone 1's matte. The colours never change.
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };
	let steel = $derived(view.lit ? { metalness: 0.75, roughness: 0.35 } : { metalness: 0, roughness: 0.6 });
	let dark = $derived(view.lit ? { metalness: 0.6, roughness: 0.55 } : { metalness: 0, roughness: 0.7 });

	let s = $derived(valveState(value, { pos, cmd }));
	let handleYaw = $derived(Math.PI / 2 - (s.pos / 100) * (Math.PI / 2));
	let body = $derived(good ? palette.steel : palette.stale);
</script>

<T.Group>
	<!-- body, flow axis vertical (the pipe rises through it) -->
	<T.Mesh castShadow receiveShadow>
		<T.SphereGeometry args={[0.045, 24, 16]} />
		<T.MeshStandardMaterial color={body} {...steel} />
	</T.Mesh>
	<T.Mesh castShadow>
		<T.CylinderGeometry args={[0.022, 0.022, 0.16, 16]} />
		<T.MeshStandardMaterial color={body} {...steel} />
	</T.Mesh>
	<!-- stem + handle -->
	<T.Mesh position={[0, 0, 0.07]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[0.006, 0.006, 0.06, 8]} />
		<T.MeshStandardMaterial color={palette.steelDark} {...dark} />
	</T.Mesh>
	<T.Group position={[0, 0, 0.1]} rotation={[0, 0, handleYaw]}>
		<T.Mesh position={[0, 0.045, 0]} castShadow>
			<T.BoxGeometry args={[0.016, 0.11, 0.01]} />
			<T.MeshStandardMaterial color={good ? palette.handle : palette.stale} metalness={view.lit ? 0.2 : 0} roughness={0.5} />
		</T.Mesh>
	</T.Group>
</T.Group>
