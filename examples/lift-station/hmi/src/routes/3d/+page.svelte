<script lang="ts">
	// The lift station in 3D — the same equipment and the same tag bindings
	// as lift-station.mimic.json, placed in metres and rendered by
	// @joyautomation/nautilus-hmi-3d. Everything about the scene is in
	// ../../../../lift-station.scene.json (at the PROJECT root, next to the
	// mimic, so `naut check` holds it to the tags); this page hands the
	// app's one realtime client to the view and carries the two things the
	// mimic shows as text — the sequence step and the alarm banner — in the
	// HUD, where a 3D drawing has no place for them.
	import { AlarmBanner, AckButton } from '@joyautomation/nautilus-hmi';
	import { SceneView, LOOKS, type SceneDoc, type SceneLook } from '@joyautomation/nautilus-hmi-3d';
	import sceneDoc from '../../../../lift-station.scene.json';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import { rt, alarms } from '$lib/client.svelte';

	// The JSON import's literal types do not overlap SceneDoc closely enough
	// for a direct cast; SceneView validates the document at run time.
	const doc = sceneDoc as unknown as SceneDoc;
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const str = (k: string) => (typeof tags[k] === 'string' ? (tags[k] as string) : '');

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

<svelte:head><title>Lift Station — 3D view</title></svelte:head>

<div class="stage">
	<SceneView {doc} {rt} {alarms} {perf} bind:look>
		{#snippet hud()}
			<div class="bar">
				<a class="pill" href="/" title="The operator screen: the P&ID mimic, faceplates, setpoints, trend">← operator screen</a>
				<button class="pill" onclick={next} title="Cycle the look: lit, flat, paper">{shown}</button>
				<span class="seq">
					Sequence <SeqStepTag text={str('SeqStep')} width={110} />
				</span>
			</div>
			<div class="bar">
				<AlarmBanner summary={alarms.summary} />
				<AckButton alarms={alarms.active} ids={['*']} onack={(ids, by) => alarms.ack(ids, by)} />
			</div>
		{/snippet}
	</SceneView>
</div>

<style>
	.stage {
		position: fixed;
		inset: 0;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.pill {
		text-decoration: none;
		font: 12px/1 var(--font, system-ui, sans-serif);
		padding: 5px 10px;
		border-radius: 999px;
		border: 1px solid var(--border, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
	.seq {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--muted, #8a887d);
	}
</style>
