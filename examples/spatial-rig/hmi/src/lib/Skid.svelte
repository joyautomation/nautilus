<script lang="ts" module>
	// A transfer skid as a KIND (docs/design/spatial-hmi.md §3d): a pump, a
	// valve and the pipe between them on a frame, reading ONE struct tag of
	// the rig's `Skid` UDT. `kind` is what the file exports about itself —
	// the UDT it reads, the members, its box — and the marker the tooling
	// recognises a kind's file by. rig.scene.json names this file under
	// kinds.skid.component and places SK101 as one node; naut check holds
	// that node to the type and members declared there.
	import { member, type KindMeta } from '@joyautomation/nautilus-hmi-3d';

	export const kind: KindMeta = {
		type: 'Skid',
		members: ['Pump', 'Valve', 'Fault'],
		bounds: { size: [0.8, 0.36, 0.26], center: [0.2, 0.15, 0] },
		labelAt: [0.2, 0.36, 0],
		status: (v, good) => {
			if (!good) return 'stale';
			if (member(v, 'Fault') === true) return 'FAULT';
			return member(member(v, 'Pump'), 'Running') === true ? 'duty' : 'standby';
		}
	};
</script>

<script lang="ts">
	// The same skid the JSON assembly `skid-data` in rig.scene.json
	// describes, written as a component: <Node> inside a kind's component is
	// a part, its `tag` a member of this node's struct, and <Pipe> reads a
	// path from it. A component can do what the data assembly cannot — the
	// frame below is plain Threlte.
	import { T } from '@threlte/core';
	import { Node, Pipe, type NodeProps } from '@joyautomation/nautilus-hmi-3d';

	let { good = true }: NodeProps = $props();
</script>

<!-- the frame: a deck and two rails, channel-steel grey -->
<T.Mesh position={[0.2, 0.005, 0]} receiveShadow>
	<T.BoxGeometry args={[0.8, 0.01, 0.26]} />
	<T.MeshStandardMaterial color={good ? '#4a4c4a' : '#5a5a58'} metalness={0.6} roughness={0.55} />
</T.Mesh>
{#each [-0.11, 0.11] as z}
	<T.Mesh position={[0.2, 0.035, z]} castShadow>
		<T.BoxGeometry args={[0.8, 0.05, 0.04]} />
		<T.MeshStandardMaterial color="#3a3c3a" metalness={0.6} roughness={0.55} />
	</T.Mesh>
{/each}

<!-- the parts, reading SK101.Pump and SK101.Valve -->
<Node id="pump" tag="Pump" kind="pump" pos={[0, 0.06, 0]} />
<Node id="valve" tag="Valve" kind="valve" pos={[0.5, 0.2, 0]} rot={[0, 0, -90]} />
<Pipe points={[[0.1, 0.16, 0], [0.1, 0.2, 0], [0.42, 0.2, 0]]} flowing="Pump.Running" />
<Pipe points={[[0.58, 0.2, 0], [0.7, 0.2, 0], [0.7, 0.05, 0]]} flowing="Valve.Pos" />
