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
	import { rt, alarms, num, has, writeTag } from '$lib/client.svelte';
	import Rain from '$lib/Rain.svelte';

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

	// Bench-only weather. InflowGpm is the bench sim's inflow (nautilus.yaml;
	// field.yaml has no sim, so the controller never publishes it there and
	// none of this shows). Rain starts above a normal day's flow and is at
	// its heaviest by the 950 gpm storm; the Storm button sets that storm, or
	// puts the inflow back to its normal 190.
	const STORM = 950;
	const NORMAL = 190;
	const simulated = $derived(has('InflowGpm'));
	const inflow = $derived(num('InflowGpm'));
	const rain = $derived(simulated ? Math.min(1, Math.max(0, (inflow - 300) / (STORM - 300))) : 0);
	const storming = $derived(inflow >= STORM - 50);

	// Lightning: while it pours, a double flash every 4 to 11 seconds.
	let flash = $state(0);
	$effect(() => {
		if (rain < 0.6) return;
		let timer: ReturnType<typeof setTimeout>;
		const seq: [number, number][] = [[1, 0], [0.15, 70], [0.85, 140], [0.3, 230], [0, 360]];
		const strike = () => {
			for (const [v, at] of seq) setTimeout(() => (flash = v), at);
			timer = setTimeout(strike, 4000 + Math.random() * 7000);
		};
		timer = setTimeout(strike, 1500 + Math.random() * 2500);
		return () => {
			clearTimeout(timer);
			flash = 0;
		};
	});
</script>

<svelte:head><title>Lift Station · 3D</title></svelte:head>

<div class="stage">
	<!-- Its own stacking layer: the scene's labels carry a very high
	     z-index, which would otherwise draw them over the panel. -->
	<div class="scene">
		<SceneView {doc} {rt} {alarms} {modules} {perf} inspector={false} bind:selected bind:look>
			{#if simulated}
				<Rain intensity={rain} {flash} />
			{/if}
			{#snippet hud()}
				<button class="pill" onclick={next} title="Cycle the look: lit, flat, paper">Look: {shown}</button>
				{#if simulated}
					<button
						class="pill"
						aria-pressed={storming}
						onclick={() => writeTag('InflowGpm', storming ? NORMAL : STORM)}
						title="Bench simulation: set the inflow to a {STORM} gpm storm, or back to {NORMAL}"
					>
						{storming ? 'Storm: on' : 'Storm'}
					</button>
				{/if}
			{/snippet}
		</SceneView>
	</div>
	<!-- The sky darkens with the rain, and lightning whitens it for a moment. -->
	<div class="sky" style:opacity={rain * 0.3} aria-hidden="true"></div>
	<div class="bolt" style:opacity={flash * 0.18} aria-hidden="true"></div>
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
	.sky,
	.bolt {
		position: absolute;
		inset: 0;
		pointer-events: none;
		transition: opacity 0.6s;
	}
	.sky {
		background: #0b1622;
		mix-blend-mode: multiply;
	}
	.bolt {
		background: #eef3ff;
		transition: none;
	}
	.pill[aria-pressed='true'] {
		border-color: #6aa9e9;
		color: #cfe4ff;
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
