<script lang="ts">
	// The lift station in 3D: the same equipment and the same tag bindings
	// as lift-station.mimic.json, placed in metres and rendered by
	// @joyautomation/nautilus-hmi-3d. Everything about the scene is in
	// ../../../../lift-station.scene.json (at the PROJECT root, next to the
	// mimic, so `naut check` holds it to the tags). Clicking equipment opens
	// the same controls as on the P&ID (EquipmentPanel), in place of the
	// view's own read-only inspector.
	import { SceneView, LOOKS, type SceneDoc, type SceneLook } from '@joyautomation/nautilus-hmi-3d';
	import sceneDoc from '../../../../lift-station.scene.json';
	// The six kinds live in files of their own (hmi/src/lib/kinds/*.kind.json,
	// design §3g): the scene names each by path, and the view pairs the
	// path with the module Vite found, as it does a component kind's. A
	// kind ejected to a .svelte beside its file (the kind editor's "Eject
	// to component") joins the same way.
	const modules = import.meta.glob(['/src/lib/kinds/*.kind.json', '/src/lib/kinds/*.svelte'], { eager: true });
	import EquipmentPanel from '$lib/EquipmentPanel.svelte';
	import { rt, alarms } from '$lib/client.svelte';

	// The JSON import's literal types do not overlap SceneDoc closely enough
	// for a direct cast; SceneView validates the document at run time.
	const doc = sceneDoc as unknown as SceneDoc;
	let selected = $state<string | null>(null);

	// The look: `lit` is the scene as authored (the glTF kinds in their own
	// paint), `flat` the same models in the kit's matte greys, `paper` the
	// station as a line drawing (design §3f). The document names none, so
	// it opens lit; `?look=flat|paper` starts elsewhere, and the HUD button
	// cycles the three. `?perf` adds the fps / latency HUD.
	const query = typeof location !== 'undefined' ? new URLSearchParams(location.search) : undefined;
	const asked = query?.get('look');
	let look = $state<SceneLook | undefined>(LOOKS.includes(asked as SceneLook) ? (asked as SceneLook) : undefined);
	let shown = $derived(look ?? doc.look ?? 'lit');
	const next = () => (look = LOOKS[(LOOKS.indexOf(shown) + 1) % LOOKS.length]);
	const perf = query?.has('perf') ?? false;
</script>

<svelte:head><title>Lift Station · 3D</title></svelte:head>

<div class="stage">
	<!-- Its own stacking layer: the scene's labels carry a very high
	     z-index, which would otherwise draw them over the panel. -->
	<div class="scene">
		<SceneView {doc} {rt} {alarms} {modules} {perf} inspector={false} bind:selected bind:look>
			{#snippet hud()}
				<button class="pill" onclick={next} title="Cycle the look: lit, flat, paper">Look: {shown}</button>
			{/snippet}
		</SceneView>
	</div>
	<EquipmentPanel bind:selected />
</div>

<style>
	.stage {
		position: absolute;
		inset: 0;
	}
	.scene {
		position: absolute;
		inset: 0;
		isolation: isolate;
	}
	.pill {
		pointer-events: auto;
		font: 12px/1 var(--font, system-ui, sans-serif);
		padding: 5px 10px;
		border-radius: 999px;
		border: 1px solid var(--border, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
</style>
