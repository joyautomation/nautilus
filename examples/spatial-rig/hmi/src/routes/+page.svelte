<script lang="ts">
	// The rig's 3D view. Everything about the scene is in ../../rig.scene.json;
	// this page only owns the two clients and hands them to the view.
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, sceneTags, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import rig from '../../../rig.scene.json';

	// The JSON import's literal types (number[] for a Vec3, a union for the
	// drives) do not overlap SceneDoc closely enough for a direct cast;
	// SceneView validates the document at run time, which is the real check.
	const doc = rig as unknown as SceneDoc;
	// The document's `kinds` may name component files (kinds.skid.component
	// → src/lib/Skid.svelte, design §3d). Vite builds every .svelte under
	// src/ into the app; the view pairs each path with its module.
	const modules = import.meta.glob('/src/lib/**/*.svelte', { eager: true });
	// One subscription, filtered to exactly the struct tags the scene reads.
	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: sceneTags(doc) });
	const alarms = createAlarmClient(rt);

	// The look: `lit` is the scene as authored (models, HDRI, shadows);
	// `flat` is the same document as grey primitives under three lights —
	// what a project with no assets gets, and the "before" of the pair.
	// `?look=flat` starts there; the HUD button flips it live.
	let look = $state<'lit' | 'flat'>(
		typeof location !== 'undefined' && new URLSearchParams(location.search).get('look') === 'flat' ? 'flat' : 'lit'
	);

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
	<SceneView {doc} {rt} {alarms} {modules} perf bind:look>
		{#snippet hud()}
			<button class="look" onclick={() => (look = look === 'lit' ? 'flat' : 'lit')} title="Switch between the lit scene and the flat, asset-free look">
				{look === 'lit' ? 'lit' : 'flat'}
			</button>
			<a class="look" href="/composed" title="The same rig, hand-written in Svelte (design §3d)">composed →</a>
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
		text-decoration: none;
		font: 12px/1 system-ui, sans-serif;
		padding: 4px 10px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
</style>
