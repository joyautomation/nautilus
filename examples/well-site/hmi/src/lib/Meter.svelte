<script lang="ts" module>
	// An inline flow meter as a KIND (§3d): a flanged body on the run and a
	// register on top. Reads ONE struct tag of the site's `Meter` UDT.
	import { member, num, type KindMeta } from '@joyautomation/nautilus-hmi-3d';

	export const kind: KindMeta = {
		type: 'Meter',
		members: ['Flow'],
		bounds: { size: [0.26, 0.26, 0.16], center: [0, 0.04, 0] },
		labelAt: [0, 0.2, 0],
		status: (v, good) => (good ? `${num(member(v, 'Flow')).toFixed(1)} L/s` : 'stale')
	};
</script>

<script lang="ts">
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type NodeProps, type Palette } from '@joyautomation/nautilus-hmi-3d';

	let { good = true }: NodeProps = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let body = $derived(good ? palette.steel : palette.stale);
</script>

<!-- the body, flow axis along x -->
<T.Mesh rotation={[0, 0, Math.PI / 2]}>
	<T.CylinderGeometry args={[0.045, 0.045, 0.22, 32]} />
	<T.MeshStandardMaterial color={body} metalness={0} roughness={0.6} />
</T.Mesh>
{#each [-0.1, 0.1] as x}
	<T.Mesh position={[x, 0, 0]} rotation={[0, 0, Math.PI / 2]}>
		<T.CylinderGeometry args={[0.06, 0.06, 0.02, 32]} />
		<T.MeshStandardMaterial color={body} metalness={0} roughness={0.6} />
	</T.Mesh>
{/each}
<!-- the register -->
<T.Mesh position={[0, 0.08, 0]}>
	<T.CylinderGeometry args={[0.04, 0.03, 0.06, 32]} />
	<T.MeshStandardMaterial color={body} metalness={0} roughness={0.6} />
</T.Mesh>
