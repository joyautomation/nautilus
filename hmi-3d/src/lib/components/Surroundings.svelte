<script lang="ts">
	// The surroundings (docs/design/spatial-hmi.md §3c): an HDRI for
	// image-based lighting and reflections, optionally a backdrop (plain, or
	// projected onto the floor so a desk-scale scene stands in it), a
	// distance fade, and a shadow-casting key light with soft shadows.
	// Realism is for the surroundings; the process state keeps its ISA-101
	// greys.
	//
	// Lighting and backdrop are two textures on purpose: a 1k HDR is plenty
	// for light and reflections, while what the eye looks at wants a sharp
	// tonemapped 4k–6k JPG, which would be a 25–100 MB file as an HDR.
	//
	// A DYNAMIC import from Scene3D: the RGBE/EXR loaders only reach the
	// browser for a document with an `environment`.
	import { T, useThrelte } from '@threlte/core';
	import { Environment, SoftShadows } from '@threlte/extras';
	import { Fog, SRGBColorSpace, type Texture } from 'three';
	import type { GroundedSkybox } from 'three/examples/jsm/objects/GroundedSkybox.js';
	import type { SceneEnvironment } from '../scene.js';

	let { env, shadowSize = 4 }: { env: SceneEnvironment; shadowSize?: number } = $props();
	const { scene } = useThrelte();

	// The skybox's ground is `height` below its origin, so lifting it by
	// that puts the photographed floor on the document's floor.
	const height = 1.6;
	let background = $derived(env.background ?? 'none');
	// The visible texture: the backdrop when given, else the HDRI itself.
	let visible = $derived(background !== 'none' ? (env.backdrop ?? env.hdri) : undefined);
	let separate = $derived(!!visible && visible !== env.hdri);
	let ground = $derived(background === 'ground' ? { height, radius: 12 } : false);

	let hdriSkybox = $state<GroundedSkybox | undefined>();
	let backdropSkybox = $state<GroundedSkybox | undefined>();
	$effect(() => {
		for (const s of [hdriSkybox, backdropSkybox]) if (s) s.position.y = (env.floor ?? 0) + height;
	});

	// A tonemapped JPG/PNG backdrop is sRGB; the loaders leave that unset.
	let backdropTexture = $state<Texture | undefined>();
	$effect(() => {
		const t = backdropTexture;
		if (t && /\.(jpe?g|png|webp)$/i.test(visible ?? '') && t.colorSpace !== SRGBColorSpace) {
			t.colorSpace = SRGBColorSpace;
			t.needsUpdate = true;
		}
	});

	$effect(() => {
		scene.environmentIntensity = env.intensity ?? 1;
		return () => {
			scene.environmentIntensity = 1;
		};
	});

	// Fog: the far backdrop and the projected floor fade toward a colour,
	// so their softness is a choice and not a resolution.
	$effect(() => {
		const f = env.fog;
		if (!f) return;
		scene.fog = new Fog(f.color ?? '#8d8a84', f.near ?? 4, f.far ?? 14);
		return () => {
			scene.fog = null;
		};
	});
</script>

{#if env.hdri}
	<!-- Lighting and reflections. It is also the backdrop when no separate one is given. -->
	<Environment
		url={env.hdri}
		isEnvironment
		isBackground={!separate && background === 'sky'}
		ground={separate ? false : ground}
		bind:skybox={hdriSkybox}
	/>
{:else}
	<T.HemisphereLight args={['#c8ccd2', '#2a2a28', 0.8]} />
{/if}
{#if separate && visible}
	<!-- The backdrop the eye sees: lights nothing. -->
	<Environment
		url={visible}
		isEnvironment={false}
		isBackground={background === 'sky'}
		{ground}
		bind:skybox={backdropSkybox}
		bind:texture={backdropTexture}
	/>
{/if}

<!-- The key light. With an HDRI it is there to cast the shadow the
     environment cannot, so it is strong, and the document's `intensity`
     turns the HDRI down to let it show (a bright workshop HDRI at 1
     swamps any lamp); without an HDRI it is the light. -->
<T.DirectionalLight
	position={[3, 5, 4]}
	intensity={env.hdri ? 3 : 1.8}
	castShadow={env.shadows ?? false}
	shadow.mapSize.width={2048}
	shadow.mapSize.height={2048}
	shadow.camera.left={-shadowSize}
	shadow.camera.right={shadowSize}
	shadow.camera.top={shadowSize}
	shadow.camera.bottom={-shadowSize}
	shadow.camera.near={0.5}
	shadow.camera.far={20}
	shadow.bias={-0.0005}
	shadow.normalBias={0.02}
/>
{#if env.shadows}
	<SoftShadows size={12} samples={12} focus={0.6} />
{/if}
