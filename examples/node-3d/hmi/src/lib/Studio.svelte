<script lang="ts">
	// Image-based light for the part library's metals: three's RoomEnvironment,
	// generated once, no asset to fetch. The scene's own lights stay on.
	import { useThrelte } from '@threlte/core';
	import { PMREMGenerator } from 'three';
	import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js';

	let { intensity = 0.6 }: { intensity?: number } = $props();
	const { scene, renderer } = useThrelte();
	const pmrem = new PMREMGenerator(renderer);
	const env = pmrem.fromScene(new RoomEnvironment(), 0.04).texture;
	scene.environment = env;
	$effect(() => {
		scene.environmentIntensity = intensity;
	});
	$effect(() => () => {
		scene.environment = null;
		env.dispose();
		pmrem.dispose();
	});
</script>
