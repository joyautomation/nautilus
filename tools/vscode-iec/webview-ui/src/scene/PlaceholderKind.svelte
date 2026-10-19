<script lang="ts">
	// What the editor draws for a kind it cannot render: a component kind
	// (its Svelte file is built by the app, not here), a kind the document
	// only declares, or a data kind whose model did not load. A box of the
	// kind's declared bounds, outlined, so the node is still there to pick,
	// drag and label — never a hole.
	import { T } from '@threlte/core';
	import { BoxGeometry } from 'three';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '@joyautomation/nautilus-hmi-3d';

	type Vec3 = [number, number, number];
	let { box = { size: [0.4, 0.4, 0.4] as Vec3, center: [0, 0.2, 0] as Vec3 } }: { box?: { size: Vec3; center: Vec3 } } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
</script>

<T.Mesh position={box.center}>
	<T.BoxGeometry args={box.size} />
	<T.MeshStandardMaterial color={palette.steel} transparent opacity={0.35} roughness={0.8} />
</T.Mesh>
<T.LineSegments position={box.center} raycast={() => {}}>
	<T.EdgesGeometry args={[new BoxGeometry(...box.size)]} />
	<T.LineBasicMaterial color={palette.steelDark} />
</T.LineSegments>
