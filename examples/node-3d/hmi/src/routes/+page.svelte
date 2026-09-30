<script lang="ts">
	// node1, opened up: the Supermicro SYS-112B-WR drawn from its chassis
	// profile crossed with its tags (docs/design/spatial-hmi.md §3e). Click
	// any part for its faceplate. Fans, PSUs and temperatures come from the
	// controller; so do drives, DIMMs, cards, the CPU and NIC ports (the
	// redfish import's part tags). Pull a drive with the plant's fault tags.
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, AlarmKey, DEFAULT_PALETTE, paletteFromTheme, type SceneCamera, type Palette } from '@joyautomation/nautilus-hmi-3d';
	import { Server, PartFaceplate, resolveParts, validateProfile, anchorPayload, OVERLAYS, cablesOverlay, linkOnPort, linkFacts, overlayColors, type ChassisProfile, type OverlayContext } from '@joyautomation/nautilus-hmi-3d/hardware';
	import QRCode from 'qrcode';
	import sys112b from '@joyautomation/nautilus-hmi-3d/profiles/supermicro-sys-112b-wr.json';
	import { plant, plantPatterns, faultTargets } from '$lib/plant';
	import Studio from '$lib/Studio.svelte';
	import ReplayClock from '$lib/ReplayClock.svelte';
	import ScenarioPanel from '$lib/ScenarioPanel.svelte';
	import PartMenu from '$lib/PartMenu.svelte';
	import { PlantFaults } from '$lib/faults.svelte';

	const NODE = 'NODE1';
	const profile = sys112b as unknown as ChassisProfile;
	const problems = validateProfile(profile);
	const parts = resolveParts(profile, NODE);
	const byId = new Map(parts.map((p) => [p.id, p]));

	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: plantPatterns() });
	const alarms = createAlarmClient(rt);
	// The plant's fault inputs: the scenario panel and a part's right-click.
	const faults = new PlantFaults();
	let menu = $state<{ x: number; y: number; title: string; simTags: string[] } | null>(null);
	function context(id: string, e: MouseEvent) {
		if (!faults.up) return;
		menu = { x: e.clientX, y: e.clientY, ...faultTargets(id, profile, parts, (rt.frame?.tags ?? {}) as Record<string, unknown>) };
	}

	// Views. The server sits with its centre on the origin.
	const params = typeof location !== 'undefined' ? new URLSearchParams(location.search) : new URLSearchParams();
	let lid = $state<'on' | 'off'>(params.get('lid') === 'on' ? 'on' : 'off');
	let xray = $state(params.has('xray'));
	let exploded = $state(params.has('exploded'));
	// The printed AR codes, where the profile's anchors put them: off by
	// default (they are a placement guide, not part of the server), drawn
	// from the same payloads as ../../labels.mjs so the model matches the sheet.
	let codes = $state(params.has('codes'));
	// One overlay at a time: heat, interfaces, what's free, cables (overlay.ts).
	const overlays = [...OVERLAYS, cablesOverlay(plant)];
	let overlayId = $state<string | null>(params.get('overlay'));
	let overlay = $derived(overlays.find((o) => o.id === overlayId));
	// The same theme read SceneView makes for the 3D parts.
	let palette = $state<Palette>(DEFAULT_PALETTE);
	// The legend's context: the same tags and colours the parts are painted
	// with (the theme's status tokens, read the way the palette reads them).
	let legendCtx = $derived<OverlayContext>({
		node: NODE,
		profile,
		tags: (rt.frame?.tags ?? {}) as Record<string, unknown>,
		colors: overlayColors(palette)
	});

	const AR_HMI = 'https://mira1.tail913f1.ts.net:9446';
	let codeImages = $state<Record<string, string>>({});
	$effect(() => {
		if (!codes || Object.keys(codeImages).length) return;
		Promise.all(
			Object.entries(profile.anchors ?? {}).map(async ([id, a]) => {
				const p = anchorPayload(a, NODE);
				const text = p === 'url' ? `${AR_HMI}/a/${NODE}` : p;
				return [id, await QRCode.toDataURL(text, { errorCorrectionLevel: 'M', margin: 1, scale: 8 })] as const;
			})
		).then((all) => (codeImages = Object.fromEntries(all)));
	});
	const D = profile.size[2] / 1000;
	const CAMERAS: Record<string, SceneCamera> = {
		iso: { pos: [0.42, 0.42, 0.62], target: [0, 0.0, 0.02], fov: 40 },
		front: { pos: [0, 0.05, 0.75], target: [0, 0.02, 0], fov: 40 },
		rear: { pos: [0, 0.08, -0.8], target: [0, 0.02, 0], fov: 40 },
		top: { pos: [0, 1.05, 0.001], target: [0, 0, 0], fov: 40 }
	};
	// A portrait phone sees a narrow slice of the same field of view, so the
	// presets pull back in proportion (the scene stays framed, not cropped).
	let aspect = $state(typeof window !== 'undefined' ? window.innerWidth / window.innerHeight : 1.6);
	const framed = (c: SceneCamera): SceneCamera => {
		const k = Math.max(1, 1.2 / aspect);
		const pos = c.pos.map((v, i) => c.target[i] + (v - c.target[i]) * k) as [number, number, number];
		return { ...c, pos };
	};
	// The fps / latency strip is a measuring tool; on a phone it crowds the
	// view, so it shows on wide screens (or with ?perf).
	let perf = $derived(params.has('perf') || aspect > 1);
	const first = params.get('view') ?? 'iso';
	let view = $state<keyof typeof CAMERAS>(first in CAMERAS ? first : 'iso');

	let selected = $state<string | null>(null);
	let open = $state(false);
	let part = $derived(selected ? byId.get(selected) : undefined);
	let tag = $derived(part ? part.tag : selected === NODE ? NODE : undefined);
	let value = $derived(tag ? (rt.frame?.tags as Record<string, unknown> | undefined)?.[tag] : undefined);
	// A port's cable: where it goes and the live check, under its facts.
	let cable = $derived.by(() => {
		if (part?.kind !== 'port') return [];
		const at = linkOnPort(plant, NODE, part.partId, profile, (rt.frame?.tags ?? {}) as Record<string, unknown>);
		return at ? linkFacts(at.check, at.near, at.link) : [{ label: 'Cable', value: 'none declared' }];
	});

	onMount(() => {
		palette = paletteFromTheme(document.body);
		rt.start();
		alarms.start();
		faults.start();
		return () => {
			faults.stop();
			alarms.stop();
			rt.stop();
		};
	});
</script>

<svelte:window onresize={() => (aspect = window.innerWidth / window.innerHeight)} />

<svelte:head><title>node1 · 3D</title></svelte:head>

<ReplayClock />
<div class="side">
	{#if (alarms.summary?.active ?? 0) > 0}
		<aside class="alarmkey" aria-label="What the alarm signs mean">
			<b>{alarms.summary?.active} in alarm</b>
			<AlarmKey {palette} />
		</aside>
	{/if}
	<ScenarioPanel {faults} />
</div>
{#if menu}
	<PartMenu {faults} {...menu} onclose={() => (menu = null)} />
{/if}

<div class="stage">
	{#if problems.length}
		<pre class="err">{JSON.stringify(problems, null, 2)}</pre>
	{/if}
	<SceneView {rt} {alarms} camera={framed(CAMERAS[view])} grid={{ pos: [0, -0.0005, 0], cell: 0.05, section: 0.25, size: [2, 2] }} inspector={false} bind:selected onselect={() => (open = true)} oncontext={context} {perf}>
		<Studio />
		<Server {profile} node={NODE} label="node1" pos={[0, 0, D / 2]} {lid} {xray} {exploded} anchor={codes} {codeImages} {overlay} />
		{#snippet hud()}
			<div class="bar" role="toolbar" aria-label="Views">
				<button class:on={lid === 'on'} onclick={() => (lid = lid === 'on' ? 'off' : 'on')}>lid {lid}</button>
				<button class:on={xray} onclick={() => (xray = !xray)}>x-ray</button>
				<button class:on={exploded} onclick={() => (exploded = !exploded)}>exploded</button>
				<button class:on={codes} onclick={() => (codes = !codes)} title="Where the printed AR codes go">codes</button>
				<span class="sep"></span>
				{#each overlays as o}
					<button class:on={overlayId === o.id} onclick={() => (overlayId = overlayId === o.id ? null : o.id)}>{o.name.toLowerCase()}</button>
				{/each}
				<span class="sep"></span>
				{#each Object.keys(CAMERAS) as v}
					<button class:on={view === v} onclick={() => (view = v as keyof typeof CAMERAS)}>{v}</button>
				{/each}
				<span class="sep"></span>
				<a class="btn" href="/rack">rack →</a>
			</div>
		{/snippet}
	</SceneView>
</div>

{#if overlay}
	<aside class="legend" aria-label="{overlay.name} legend">
		<b>{overlay.name}</b>
		<ul>
			{#each overlay.legend(legendCtx) as item}
				<li>
					{#if item.color}<i style:background={item.color}></i>{/if}
					<span>{item.label}</span>
				</li>
			{/each}
		</ul>
		<p>{overlay.caption}</p>
	</aside>
{/if}

{#if open && selected}
	<PartFaceplate
		{part}
		server={{ label: 'node1 · Supermicro SYS-112B-WR', tag: NODE }}
		{value}
		quality={tag ? rt.quality(tag) : 'good'}
		extra={cable}
		onclose={() => {
			open = false;
			selected = null;
		}}
	/>
{/if}

<style>
	.stage {
		position: fixed;
		inset: 0;
	}
	.bar {
		display: flex;
		gap: 6px;
		align-items: center;
		flex-wrap: wrap;
	}
	.sep {
		width: 8px;
	}
	button,
	.btn {
		font: 12px/1 system-ui, sans-serif;
		text-decoration: none;
		padding: 5px 10px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
	button.on {
		border-color: var(--accent, #6aa5e8);
		color: var(--accent, #6aa5e8);
	}
	.legend {
		position: fixed;
		left: 12px;
		bottom: 12px;
		max-width: 300px;
		padding: 10px 12px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.4 system-ui, sans-serif;
	}
	.legend ul {
		list-style: none;
		margin: 6px 0;
		padding: 0;
		display: grid;
		gap: 3px;
	}
	.legend li {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.legend i {
		width: 12px;
		height: 12px;
		border-radius: 3px;
		flex: none;
	}
	.legend p {
		margin: 0;
		color: var(--ink-2, #a8a6a1);
		font-size: 11px;
	}
	.err {
		position: absolute;
		right: 12px;
		top: 12px;
		z-index: 2;
		color: var(--crit, #d03b3b);
	}
	/* The alarm signs' key, while anything is in alarm: top right, clear of
	   the toolbar and the overlay legend. */
	/* Top right: the alarm key, then the simulation's scenarios. */
	.side {
		position: fixed;
		top: 12px;
		right: 12px;
		/* clear of the replay clock */
		bottom: 96px;
		z-index: 5;
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 8px;
		pointer-events: none;
	}
	.alarmkey {
		pointer-events: auto;
		display: grid;
		gap: 6px;
		padding: 8px 10px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.3 system-ui, sans-serif;
	}
</style>
