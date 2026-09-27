<script lang="ts">
	// The rig, hand-written in Svelte (docs/design/spatial-hmi.md §3d): the
	// same live controller and the same look as the document at `/`, but
	// every node is a <Node> in this file — composed the way a SvelteKit
	// page composes, with the built-ins, a component used as a component
	// (the Skid), and a document kind placed by name. What this page gives
	// up is the tools' half: nothing here is checked by `naut check`, listed
	// in a palette, dragged or anchored, because it is not data.
	//
	// The kinds, the surroundings and the camera ARE data, and stay in the
	// document: this page imports them from rig.scene.json and brings only
	// its own nodes, pipes and fixtures.
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, Node, Pipe, Fixture3D, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import Skid, { kind as skidKind } from '$lib/Skid.svelte';
	import rig from '../../../../rig.scene.json';

	const src = rig as unknown as SceneDoc;
	// Only the document's kinds and surroundings; the nodes are below.
	const doc: SceneDoc = { name: 'Office rig, composed', kinds: src.kinds, environment: src.environment, camera: src.camera, grid: src.grid, nodes: [] };
	const modules = import.meta.glob('/src/**/*.svelte', { eager: true });
	// A page that lists its own tags: the scene subscribes to what it shows.
	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: ['T101', 'P101', 'XV101', 'SK101', 'SK102', 'Demand'] });
	const alarms = createAlarmClient(rt);

	let look = $state<'lit' | 'flat'>('lit');

	onMount(() => {
		rt.start();
		alarms.start();
		return () => {
			alarms.stop();
			rt.stop();
		};
	});
</script>

<svelte:head><title>{doc.name} · 3D</title></svelte:head>

<div class="stage">
	<SceneView {doc} {rt} {alarms} {modules} perf bind:look>
		<!-- the floor and the desk: a top and four legs -->
		<Fixture3D fixture={{ kind: 'plane', pos: [0.9, -0.749, 0.2], size: [4, 3], texture: { map: 'textures/concrete_floor_02_diff_1k.jpg', normalMap: 'textures/concrete_floor_02_nor_gl_1k.jpg', roughnessMap: 'textures/concrete_floor_02_rough_1k.jpg', repeat: [4, 3] } }} />
		<Fixture3D fixture={{ kind: 'box', pos: [0.35, -0.01, 0.1], size: [0.9, 0.02, 0.6], color: '#3a3833' }} />
		{#each [[-0.07, -0.17], [0.77, -0.17], [-0.07, 0.37], [0.77, 0.37]] as [x, z]}
			<Fixture3D fixture={{ kind: 'box', pos: [x, -0.385, z], size: [0.04, 0.73, 0.04], color: '#3a3833' }} />
		{/each}
		<Fixture3D fixture={{ kind: 'marker', pos: [0, 0.001, 0], size: [0.08, 0.08] }} />

		<!-- the three props, by kind: the document's glTF kinds in the lit look, the built-ins in the flat -->
		<Node id="T101" tag="T101" kind="tank" label="T-101" pos={[1.0, -0.75, 0.4]} />
		<Node id="P101" tag="P101" kind="pump" label="P-101" pos={[0.3, 0, 0.2]} />
		<Node id="XV101" tag="XV101" kind="valve" label="XV-101" pos={[1.8, 0.85, 0]} bind={{ cmd: 'Demand' }} />
		<Pipe points={[[0.46, 0.08, 0.2], [0.75, 0.08, 0.2], [0.75, 0.08, 0.4], [1.0, 0.08, 0.4], [1.0, -0.33, 0.4]]} flowing="P101.Running" />
		<Pipe points={[[1.2, -0.7, 0.4], [1.8, -0.7, 0.4], [1.8, 0.75, 0.4], [1.8, 0.75, 0]]} flowing="XV101.Pos" />

		<!-- the skids: a component used as a component (its `kind` export lends the
		     status text), and a document kind placed by name -->
		<Node id="SK101" tag="SK101" label="SK-101" pos={[-0.6, -0.75, 0.7]} status={skidKind.status}>
			{#snippet children(p)}<Skid {...p} />{/snippet}
		</Node>
		<Node id="SK102" tag="SK102" kind="skid-data" label="SK-102" pos={[-0.6, -0.75, 1.25]} />

		{#snippet hud()}
			<button class="look" onclick={() => (look = look === 'lit' ? 'flat' : 'lit')}>{look}</button>
			<a class="look" href="/" title="The same rig from rig.scene.json">← document</a>
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
