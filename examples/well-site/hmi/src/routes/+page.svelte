<script lang="ts">
	// The well, drawn. Everything about the scene is in ../../well.scene.json
	// — the paper look and the isometric camera included (design §3f); this
	// page only owns the two clients and hands them to the view.
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, sceneTags, LOOKS, type SceneDoc, type SceneLook } from '@joyautomation/nautilus-hmi-3d';
	import well from '../../../well.scene.json';

	const doc = well as unknown as SceneDoc;
	const modules = import.meta.glob('/src/lib/**/*.svelte', { eager: true });
	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: sceneTags(doc) });
	const alarms = createAlarmClient(rt);

	// The look: the document asks for paper; `?look=lit|flat` starts
	// elsewhere, and the HUD button cycles through the three.
	const asked = typeof location !== 'undefined' ? new URLSearchParams(location.search).get('look') : null;
	let look = $state<SceneLook | undefined>(LOOKS.includes(asked as SceneLook) ? (asked as SceneLook) : undefined);
	let shown = $derived(look ?? doc.look ?? 'lit');
	const next = () => (look = LOOKS[(LOOKS.indexOf(shown) + 1) % LOOKS.length]);

	onMount(() => {
		rt.start();
		alarms.start();
		return () => {
			alarms.stop();
			rt.stop();
		};
	});
</script>

<svelte:head><title>{doc.name ?? 'Well'} · 3D</title></svelte:head>

<div class="stage">
	<SceneView {doc} {rt} {alarms} {modules} perf bind:look>
		{#snippet hud()}
			<button class="look" onclick={next} title="Cycle the look: paper, lit, flat">{shown}</button>
		{/snippet}
	</SceneView>
</div>

<style>
	.stage {
		position: fixed;
		inset: 0;
	}
	.look {
		align-self: flex-start;
		font: 12px/1 system-ui, sans-serif;
		padding: 4px 10px;
		border-radius: 999px;
		border: 1px solid var(--axis, #d6d6d1);
		background: color-mix(in srgb, var(--surface, #ffffff) 85%, transparent);
		color: var(--ink, #1b1a17);
		cursor: pointer;
	}
</style>
