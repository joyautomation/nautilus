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
	import { SceneView, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import sceneDoc from '../../../../lift-station.scene.json';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import { rt, alarms } from '$lib/client.svelte';

	// The JSON import's literal types do not overlap SceneDoc closely enough
	// for a direct cast; SceneView validates the document at run time.
	const doc = sceneDoc as unknown as SceneDoc;
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const str = (k: string) => (typeof tags[k] === 'string' ? (tags[k] as string) : '');

	// `lit` is the scene as authored (the glTF kinds); `flat` is the same
	// document as the kit's grey primitives, which have no wet well or
	// float, so it is the honest "before" of the pair rather than a view
	// to operate from. `?perf` adds the fps / latency HUD.
	let look = $state<'lit' | 'flat'>('lit');
	const perf = typeof location !== 'undefined' && new URLSearchParams(location.search).has('perf');
</script>

<svelte:head><title>Lift Station — 3D view</title></svelte:head>

<div class="stage">
	<SceneView {doc} {rt} {alarms} {perf} bind:look>
		{#snippet hud()}
			<div class="bar">
				<a class="pill" href="/" title="The operator screen: the P&ID mimic, faceplates, setpoints, trend">← operator screen</a>
				<button class="pill" onclick={() => (look = look === 'lit' ? 'flat' : 'lit')} title="Switch between the modelled scene and the kit's flat primitives">
					{look}
				</button>
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
