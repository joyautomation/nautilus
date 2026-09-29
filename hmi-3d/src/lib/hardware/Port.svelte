<script lang="ts" module>
	// A network port: the mouth of an SFP cage or an RJ45 jack, at the rear.
	// The card or board draws the jack itself; a port adds a thin plate over
	// its mouth, which lights when an overlay has something to say, and a
	// box that takes the pick. Bound to a NetPort UDT (LinkUp, SpeedGbps…);
	// the profile's `ratedGbps` is what the port can run at.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'NetPort',
		members: ['Name', 'LinkUp', 'SpeedGbps', 'MAC'],
		bounds: { size: [0.014, 0.01, 0.012], center: [0, 0, 0] },
		status: (v, good) => partStatus('port', v, partState({ tag: 'port' }, v, good))
	};
</script>

<script lang="ts">
	import { T } from '@threlte/core';
	import type { PartProps } from './types.js';

	let { size, paint }: PartProps = $props();
	const noRaycast = () => {};
	// The mouth faces the rear: -z, the chassis frame's back.
	let mouth = $derived<[number, number, number]>([0, 0, -size[2] / 2 - 0.0004]);
</script>

{#if paint?.color && !paint.dim}
	<T.Mesh position={mouth} rotation={[0, Math.PI, 0]} raycast={noRaycast}>
		<T.PlaneGeometry args={[size[0] * 0.9, size[1] * 0.9]} />
		<T.MeshBasicMaterial color={paint.color} />
	</T.Mesh>
	<!-- and a faint body, so the port reads from above as well as behind -->
	<T.Mesh raycast={noRaycast}>
		<T.BoxGeometry args={size} />
		<T.MeshBasicMaterial color={paint.color} transparent opacity={0.35} depthWrite={false} />
	</T.Mesh>
{/if}
<T.Mesh>
	<T.BoxGeometry args={size} />
	<T.MeshBasicMaterial visible={false} />
</T.Mesh>
