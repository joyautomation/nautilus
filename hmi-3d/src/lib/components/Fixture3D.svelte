<script lang="ts">
	// Static, unbound geometry from the document: a desk top, a floor slab,
	// the origin marker. Context for the eye, nothing live. A plane may
	// carry PBR maps (a concrete floor); in the flat look it stays a colour.
	import { T } from '@threlte/core';
	import { useTexture } from '@threlte/extras';
	import { RepeatWrapping, SRGBColorSpace, NoColorSpace, type MeshStandardMaterial, type Texture } from 'three';
	import { getContext } from 'svelte';
	import type { SceneFixture } from '../scene.js';
	import type { ViewState } from './view.js';

	let { fixture }: { fixture: SceneFixture } = $props();
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };
	const deg = Math.PI / 180;
	let rot = $derived<[number, number, number]>(
		fixture.rot ? [fixture.rot[0] * deg, fixture.rot[1] * deg, fixture.rot[2] * deg] : [0, 0, 0]
	);
	let size = $derived(fixture.size ?? (fixture.kind === 'box' ? [1, 1, 1] : fixture.kind === 'marker' ? [0.08, 0.08] : [1, 1]));
	let opacity = $derived(fixture.opacity ?? 1);

	// Textures load only in the lit look, and only for a plane that asks.
	// Wrap, repeat and colour space are set before the first upload (in
	// the loader's transform), because a change after it needs another.
	let tex = $derived(view.lit && fixture.kind === 'plane' ? fixture.texture : undefined);
	let maps = $state<{ map?: Texture; normalMap?: Texture; roughnessMap?: Texture }>({});
	$effect(() => {
		const t = tex;
		if (!t) {
			maps = {};
			return;
		}
		const [rx, ry] = t.repeat ?? [1, 1];
		const load = (url: string, srgb: boolean) =>
			useTexture(url, {
				transform: (tx) => {
					tx.wrapS = tx.wrapT = RepeatWrapping;
					tx.repeat.set(rx, ry);
					tx.colorSpace = srgb ? SRGBColorSpace : NoColorSpace;
					tx.needsUpdate = true;
					return tx;
				}
			});
		let cancelled = false;
		Promise.all([load(t.map, true), t.normalMap ? load(t.normalMap, false) : undefined, t.roughnessMap ? load(t.roughnessMap, false) : undefined]).then(
			([map, normalMap, roughnessMap]) => {
				if (!cancelled) maps = { map, normalMap, roughnessMap };
			}
		);
		return () => {
			cancelled = true;
		};
	});

	// One material whose maps are props: a map added after the first compile
	// needs the shader rebuilt, which three does on needsUpdate. (Swapping
	// material components in an {#if} instead leaves the mesh on three's
	// default material once the old one detaches.)
	let mat = $state<MeshStandardMaterial>();
	$effect(() => {
		void maps;
		if (mat) mat.needsUpdate = true;
	});
</script>

{#if fixture.kind === 'box'}
	<T.Mesh position={fixture.pos} rotation={rot} castShadow receiveShadow>
		<T.BoxGeometry args={[size[0], size[1] ?? 1, size[2] ?? 1]} />
		<T.MeshStandardMaterial
			color={fixture.color ?? '#3a3833'}
			transparent={opacity < 1}
			{opacity}
			metalness={view.lit ? 0.1 : 0}
			roughness={view.lit ? 0.7 : 0.6}
		/>
	</T.Mesh>
{:else}
	<!-- planes and markers lie flat unless rotated: the y-up frame's ground -->
	<T.Mesh position={fixture.pos} rotation={[-Math.PI / 2 + rot[0], rot[1], rot[2]]} receiveShadow>
		<T.PlaneGeometry args={[size[0], size[1] ?? size[0]]} />
		{#if fixture.kind === 'marker'}
			<T.MeshBasicMaterial color={fixture.color ?? '#e8e6e1'} transparent={opacity < 1} {opacity} side={2} />
		{:else}
			<T.MeshStandardMaterial
				bind:ref={mat}
				map={maps.map}
				normalMap={maps.normalMap}
				roughnessMap={maps.roughnessMap}
				color={fixture.color ?? (maps.map ? '#ffffff' : '#2a2a28')}
				transparent={opacity < 1}
				{opacity}
				metalness={0}
				roughness={maps.roughnessMap ? 1 : 0.9}
				side={2}
			/>
		{/if}
	</T.Mesh>
{/if}
