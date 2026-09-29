<script lang="ts">
	// node1, opened up: the Supermicro SYS-112B-WR drawn from its chassis
	// profile crossed with its tags (docs/design/spatial-hmi.md §3e). Click
	// any part for its faceplate. Fans, PSUs and temperatures come from the
	// controller; drives, DIMMs, cards and the CPU are stubbed from the BMC
	// capture until the drivers publish them (../lib/stub.ts).
	import { onMount } from 'svelte';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, DEFAULT_PALETTE, paletteFromTheme, type SceneCamera, type Palette } from '@joyautomation/nautilus-hmi-3d';
	import { Server, PartFaceplate, resolveParts, serverTags, validateProfile, anchorPayload, OVERLAYS, overlayColors, type ChassisProfile, type OverlayContext } from '@joyautomation/nautilus-hmi-3d/hardware';
	import QRCode from 'qrcode';
	import sys112b from '@joyautomation/nautilus-hmi-3d/profiles/supermicro-sys-112b-wr.json';
	import stubJson from '$lib/node1.stub.json';
	import { stubbed, isStubTag, type Stub } from '$lib/stub';
	import Studio from '$lib/Studio.svelte';

	const NODE = 'NODE1';
	const profile = sys112b as unknown as ChassisProfile;
	const stub = structuredClone(stubJson) as Stub;
	const problems = validateProfile(profile);
	const parts = resolveParts(profile, NODE);
	const byId = new Map(parts.map((p) => [p.id, p]));

	const real = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: [...serverTags(profile, NODE), `${NODE}_Temp_*`] });
	const rt = stubbed(real, stub);
	const alarms = createAlarmClient(real);

	// Views. The server sits with its centre on the origin.
	const params = typeof location !== 'undefined' ? new URLSearchParams(location.search) : new URLSearchParams();
	// `?pull=NODE1_Drive_NVMe2` shows a pulled drive (Present = false) until
	// the real tags exist to pull one on the bench.
	for (const t of params.getAll('pull')) if (stub.tags[t]) stub.tags[t].Present = false;
	let lid = $state<'on' | 'off'>(params.get('lid') === 'on' ? 'on' : 'off');
	let xray = $state(params.has('xray'));
	let exploded = $state(params.has('exploded'));
	// The printed AR codes, where the profile's anchors put them: off by
	// default (they are a placement guide, not part of the server), drawn
	// from the same payloads as ../../labels.mjs so the model matches the sheet.
	let codes = $state(params.has('codes'));
	// One overlay at a time: heat, interfaces, what's free (overlay.ts).
	let overlayId = $state<string | null>(params.get('overlay'));
	let overlay = $derived(OVERLAYS.find((o) => o.id === overlayId));
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

	onMount(() => {
		palette = paletteFromTheme(document.body);
		real.start();
		alarms.start();
		return () => {
			alarms.stop();
			real.stop();
		};
	});
</script>

<svelte:window onresize={() => (aspect = window.innerWidth / window.innerHeight)} />

<svelte:head><title>node1 · 3D</title></svelte:head>

<div class="stage">
	{#if problems.length}
		<pre class="err">{JSON.stringify(problems, null, 2)}</pre>
	{/if}
	<SceneView {rt} {alarms} camera={framed(CAMERAS[view])} grid={{ pos: [0, -0.0005, 0], cell: 0.05, section: 0.25, size: [2, 2] }} inspector={false} bind:selected onselect={() => (open = true)} {perf}>
		<Studio />
		<Server {profile} node={NODE} label="node1" pos={[0, 0, D / 2]} {lid} {xray} {exploded} anchor={codes} {codeImages} {overlay} />
		{#snippet hud()}
			<div class="bar" role="toolbar" aria-label="Views">
				<button class:on={lid === 'on'} onclick={() => (lid = lid === 'on' ? 'off' : 'on')}>lid {lid}</button>
				<button class:on={xray} onclick={() => (xray = !xray)}>x-ray</button>
				<button class:on={exploded} onclick={() => (exploded = !exploded)}>exploded</button>
				<button class:on={codes} onclick={() => (codes = !codes)} title="Where the printed AR codes go">codes</button>
				<span class="sep"></span>
				{#each OVERLAYS as o}
					<button class:on={overlayId === o.id} onclick={() => (overlayId = overlayId === o.id ? null : o.id)}>{o.name.toLowerCase()}</button>
				{/each}
				<span class="sep"></span>
				{#each Object.keys(CAMERAS) as v}
					<button class:on={view === v} onclick={() => (view = v as keyof typeof CAMERAS)}>{v}</button>
				{/each}
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
		note={isStubTag(stub, tag) ? `Stub: ${stub.source}.` : undefined}
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
	button {
		font: 12px/1 system-ui, sans-serif;
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
</style>
