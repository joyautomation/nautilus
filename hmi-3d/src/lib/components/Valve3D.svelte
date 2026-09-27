<script lang="ts">
	// A quarter-turn valve on a vertical run. The handle lies across the
	// pipe when closed and along it when open, following Pos.
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { valveState } from '../kinds.js';
	import type { NodeProps } from '../registry.js';

	let { value, good = true, pos, cmd }: NodeProps & { pos?: unknown; cmd?: unknown } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	let s = $derived(valveState(value, { pos, cmd }));
	let handleYaw = $derived(Math.PI / 2 - (s.pos / 100) * (Math.PI / 2));
	let body = $derived(good ? palette.steel : palette.stale);
</script>

<T.Group>
	<!-- body, flow axis vertical (the pipe rises through it) -->
	<T.Mesh>
		<T.SphereGeometry args={[0.045, 24, 16]} />
		<T.MeshStandardMaterial color={body} />
	</T.Mesh>
	<T.Mesh>
		<T.CylinderGeometry args={[0.022, 0.022, 0.16, 16]} />
		<T.MeshStandardMaterial color={body} />
	</T.Mesh>
	<!-- stem + handle -->
	<T.Mesh position={[0, 0, 0.07]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[0.006, 0.006, 0.06, 8]} />
		<T.MeshStandardMaterial color={palette.steelDark} />
	</T.Mesh>
	<T.Group position={[0, 0, 0.1]} rotation={[0, 0, handleYaw]}>
		<T.Mesh position={[0, 0.045, 0]}>
			<T.BoxGeometry args={[0.016, 0.11, 0.01]} />
			<T.MeshStandardMaterial color={good ? palette.handle : palette.stale} />
		</T.Mesh>
	</T.Group>
</T.Group>
