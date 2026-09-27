<script lang="ts">
	// The rig's 3D view. Everything about the scene is in ../../rig.scene.json;
	// this page only owns the two clients and hands them to the view.
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, sceneTags, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import rig from '../../../rig.scene.json';

	const doc = rig as SceneDoc;
	// One subscription, filtered to exactly the struct tags the scene reads.
	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: sceneTags(doc) });
	const alarms = createAlarmClient(rt);

	onMount(() => {
		rt.start();
		alarms.start();
		return () => {
			alarms.stop();
			rt.stop();
		};
	});
</script>

<svelte:head><title>{doc.name ?? 'Rig'} · 3D</title></svelte:head>

<div class="stage">
	<SceneView {doc} {rt} {alarms} perf />
</div>

<style>
	.stage {
		position: fixed;
		inset: 0;
	}
</style>
