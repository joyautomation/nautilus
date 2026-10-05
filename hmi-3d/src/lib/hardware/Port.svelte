<script lang="ts" module>
	// A network port: the mouth of an SFP cage or an RJ45 jack, at the rear.
	// The card or board draws the jack itself; a port adds a thin plate over
	// its mouth, which lights when an overlay has something to say, and a
	// box that takes the pick. Bound to a NetPort UDT (LinkUp, SpeedGbps…);
	// the profile's `ratedGbps` is what the port can run at.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus, portReading } from './profile.js';
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

	let { size, paint, panel = false, value }: PartProps & { panel?: boolean } = $props();
	// A port set in a front panel (a switch) has no card to draw its jack:
	// it draws its own, and the link LED beside it.
	let up = $derived(portReading(value).up === true);
	const noRaycast = () => {};
	// The mouth faces the rear: -z, the chassis frame's back.
	let mouth = $derived<[number, number, number]>([0, 0, -size[2] / 2 - 0.0004]);
</script>

{#if panel}
	<T.Mesh raycast={noRaycast}>
		<T.BoxGeometry args={[size[0] * 0.92, size[1] * 0.85, size[2]]} />
		<T.MeshStandardMaterial color="#0e0f10" roughness={0.9} />
	</T.Mesh>
	{#if up && !paint}
		<T.Mesh position={[size[0] * 0.3, size[1] * 0.5 + 0.0012, -size[2] / 2 - 0.0005]} rotation={[0, Math.PI, 0]} raycast={noRaycast}>
			<T.PlaneGeometry args={[0.0022, 0.0014]} />
			<T.MeshBasicMaterial color="#3fbf5f" />
		</T.Mesh>
	{/if}
{/if}
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
