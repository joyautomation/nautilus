<script lang="ts">
	// Static, unbound geometry from the document: a desk top, a floor slab,
	// the origin marker. Context for the eye, nothing live.
	import { T } from '@threlte/core';
	import type { SceneFixture } from '../scene.js';

	let { fixture }: { fixture: SceneFixture } = $props();
	const deg = Math.PI / 180;
	let rot = $derived<[number, number, number]>(
		fixture.rot ? [fixture.rot[0] * deg, fixture.rot[1] * deg, fixture.rot[2] * deg] : [0, 0, 0]
	);
	let size = $derived(fixture.size ?? (fixture.kind === 'box' ? [1, 1, 1] : fixture.kind === 'marker' ? [0.08, 0.08] : [1, 1]));
	let opacity = $derived(fixture.opacity ?? 1);
</script>

{#if fixture.kind === 'box'}
	<T.Mesh position={fixture.pos} rotation={rot}>
		<T.BoxGeometry args={[size[0], size[1] ?? 1, size[2] ?? 1]} />
		<T.MeshStandardMaterial color={fixture.color ?? '#3a3833'} transparent={opacity < 1} {opacity} />
	</T.Mesh>
{:else}
	<!-- planes and markers lie flat unless rotated: the y-up frame's ground -->
	<T.Mesh position={fixture.pos} rotation={[-Math.PI / 2 + rot[0], rot[1], rot[2]]}>
		<T.PlaneGeometry args={[size[0], size[1] ?? size[0]]} />
		{#if fixture.kind === 'marker'}
			<T.MeshBasicMaterial color={fixture.color ?? '#e8e6e1'} transparent={opacity < 1} {opacity} side={2} />
		{:else}
			<T.MeshStandardMaterial color={fixture.color ?? '#2a2a28'} transparent={opacity < 1} {opacity} side={2} />
		{/if}
	</T.Mesh>
{/if}
