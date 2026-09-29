<script lang="ts">
	// The office cluster as it is racked: three SYS-112B-WR nodes and three
	// S3900 switches, each drawn from its profile, placed by the rack layout
	// ($lib/hq.rack.json), with every declared cable from the topology drawn
	// between its exact ports and coloured by its live check (topology.ts).
	// Click a device to zoom into it — the node view's lid, x-ray, exploded
	// and overlays, for that device — and a part for its faceplate.
	import { onMount } from 'svelte';
	import { Tween } from 'svelte/motion';
	import { cubicOut } from 'svelte/easing';
	import { RealtimeClient, createAlarmClient, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView, DEFAULT_PALETTE, paletteFromTheme, type SceneCamera, type Palette, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import {
		Server,
		Rack,
		Cable,
		NetworkMesh,
		PartFaceplate,
		resolveParts,
		OVERLAYS,
		cablesOverlay,
		linkOnPort,
		linkFacts,
		checkAll,
		overlayColors,
		verdictColor,
		placeDevice,
		toRack,
		endInRack,
		portMouth,
		portPart,
		focusFade,
		neighbourhood,
		type Placement,
		cablePath,
		parseEnd,
		portReading,
		VERDICT_MARK,
		type RackLayout,
		type ServerPart,
		type Verdict,
		type OverlayContext
	} from '@joyautomation/nautilus-hmi-3d/hardware';
	import { stubbed, isStubTag } from '$lib/stub';
	import { plant, allStubs, plantPatterns, topology } from '$lib/plant';
	import hqRack from '$lib/hq.rack.json';
	import Studio from '$lib/Studio.svelte';
	import ReplayClock from '$lib/ReplayClock.svelte';

	const layout = hqRack as RackLayout;
	const stub = allStubs();
	const real = new RealtimeClient<NautilusFrame>({ url: '/api/stream', tags: plantPatterns() });
	const rt = stubbed(real, stub);
	const alarms = createAlarmClient(real);

	// Every racked device with its profile, placement and parts.
	const devices = layout.devices.flatMap((r) => {
		const d = topology.devices.find((x) => x.id === r.id);
		const profile = d && plant.profileOf(d);
		if (!d || !profile) return [];
		return [{ ...d, profile, place: placeDevice(layout, r), parts: resolveParts(profile, d.tag) }];
	});
	const byTag = new Map(devices.map((d) => [d.tag, d]));
	const partById = new Map<string, ServerPart>(devices.flatMap((d) => d.parts.map((p) => [p.id, p] as [string, ServerPart])));
	const profileOfId = (id: string) => devices.find((d) => d.id === id)?.profile;

	const params = typeof location !== 'undefined' ? new URLSearchParams(location.search) : new URLSearchParams();
	let tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	let palette = $state<Palette>(DEFAULT_PALETTE);
	let colors = $derived(overlayColors(palette));

	// ── cables ────────────────────────────────────────────────────────
	let showCables = $state(!params.has('nocables'));
	let checks = $derived(checkAll(plant, tags));
	// ── focus: the rack, or one device in it ──────────────────────────
	// (mesh first: the slides read it.)
	let mesh = $state(params.has('mesh'));
	let focus = $state<string | null>(params.get('focus'));
	let focused = $derived(focus ? byTag.get(focus) : undefined);
	let hood = $derived(focused ? neighbourhood(topology, focused.id) : undefined);
	// The focused server slides out on its rails, the way it is pulled to
	// be serviced: its parts get room, it stays in its slot and on its cables.
	const SLIDE = 0.45;
	const slides = new Map(devices.map((d) => [d.id, Tween.of(() => (!mesh && focus === d.tag && d.kind === 'server' ? SLIDE : 0), { duration: 700, easing: cubicOut })]));
	const slid = (p: Placement, s: number): Placement => ({ ...p, pos: [p.pos[0], p.pos[1], p.pos[2] + (p.rotY === 180 ? -s : s)] });
	const placeOf = (d: (typeof devices)[number]) => slid(d.place, slides.get(d.id)?.current ?? 0);

	// The drawn path of each link, following its ends: lanes numbered per
	// side (fixed by the layout) so cables down the same side sit apart. A
	// path is only rebuilt when an end moves.
	const mouth = (s: string) => {
		const e = parseEnd(s);
		const d = devices.find((x) => x.id === e.device);
		const part = d && e.port ? portPart(d.profile, e.port) : undefined;
		return d && part ? portMouth(placeOf(d), d.profile, part) : undefined;
	};
	const lanes = (() => {
		const n: Record<string, number> = {};
		return topology.links.map((link) => {
			const a = parseEnd(link.a);
			const b = parseEnd(link.b);
			const ea = a.port ? endInRack(layout, profileOfId, a.device, a.port) : undefined;
			const eb = b.port ? endInRack(layout, profileOfId, b.device, b.port) : undefined;
			if (!ea) return 0;
			const side = (ea.at[0] + (eb?.at[0] ?? ea.at[0]) >= 0 ? 'r' : 'l') + (eb ? '' : 'up');
			return (n[side] = (n[side] ?? -1) + 1);
		});
	})();
	const memo = new Map<number, { key: string; pts: Vec3[] }>();
	let paths = $derived(
		topology.links.map((link, i) => {
			const ea = mouth(link.a);
			if (!ea) return undefined;
			const eb = mouth(link.b);
			const pts = cablePath(layout, ea, eb, lanes[i]);
			const key = pts.flat().map((v) => v.toFixed(4)).join();
			const m = memo.get(i);
			if (m?.key === key) return m.pts;
			memo.set(i, { key, pts });
			return pts;
		})
	);
	let counts = $derived(
		checks.reduce<Record<Verdict, number>>((n, c) => ({ ...n, [c.check.verdict]: n[c.check.verdict] + 1 }), { confirmed: 0, consistent: 0, contradicted: 0, down: 0, unverified: 0 })
	);

	// ── mesh: the same devices and links as a network ─────────────────
	let edgeLabels = $state(true);
	const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
	// A line under each device in the mesh: ports with link on a switch,
	// power on a server — or that nothing reports.
	let summary = $derived(
		Object.fromEntries(
			devices.map((d) => {
				const v = tags[d.tag] as Record<string, unknown> | undefined;
				if (d.kind === 'switch') {
					const ports = d.parts.filter((p) => p.tag && tags[p.tag] !== undefined);
					if (!ports.length) return [d.id, 'not reported'];
					return [d.id, `${ports.filter((p) => portReading(tags[p.tag!]).up === true).length}/${ports.length} ports up`];
				}
				const w = num(v?.PowerW);
				return [d.id, v ? (w !== undefined ? `${Math.round(w)} W · ${v.Online === false ? 'BMC dark' : 'online'}` : 'online') : 'not reported'];
			})
		)
	);
	// Focus in the mesh stays in the mesh: the device, its links and what
	// they reach stand out; "show in rack" (or a second click) goes physical.
	let meshFocus = $state<string | null>(params.get('mesh') || null);
	const MESH: SceneCamera = { pos: [1.55, 2.3, 2.1], target: [0, 0.7, 0], fov: 40 };

	let lid = $state<'on' | 'off'>('off');
	let xray = $state(false);
	let exploded = $state(false);
	const overlays = [...OVERLAYS, cablesOverlay(plant)];
	let overlayId = $state<string | null>(params.get('overlay'));
	let overlay = $derived(overlays.find((o) => o.id === overlayId));

	const top = layout.base! / 1000 + (layout.units * 44.45) / 1000;
	const D = layout.depth / 1000;
	const RACK: Record<string, SceneCamera> = {
		cables: { pos: [-1.25, top + 0.25, -D - 1.35], target: [0, top * 0.62, -D / 2], fov: 40 },
		rear: { pos: [0, top * 0.75, -D - 2.1], target: [0, top * 0.55, -D / 2], fov: 40 },
		front: { pos: [0.5, top * 0.8, 1.9], target: [0, top * 0.55, -D / 2], fov: 40 }
	};
	const firstView = params.get('view') ?? 'cables';
	let view = $state(firstView in RACK ? firstView : 'cables');
	// Zoomed in: a server from above its rear I/O (lid off, the ports and
	// their cables in view); a switch square to its port face.
	const closeUp = (d: (typeof devices)[number]): SceneCamera => {
		if (d.kind === 'switch') {
			const t = toRack(d.place, [0, 0.022, 0]);
			return { pos: [t[0] + 0.12, t[1] + 0.16, t[2] + (d.place.rotY === 180 ? -0.62 : 0.62)], target: t, fov: 40 };
		}
		// From the front and above: slid out on its rails, nothing is over
		// it, so the open lid shows every part; the rear I/O and its cables
		// run back into the rack behind.
		const t = toRack(slid(d.place, SLIDE), [0, 0.02, -0.25]);
		const front = d.place.rotY === 180 ? -1 : 1;
		return { pos: [t[0] + 0.3, t[1] + 0.62, t[2] + front * 0.62], target: t, fov: 40 };
	};
	let aspect = $state(typeof window !== 'undefined' ? window.innerWidth / window.innerHeight : 1.6);
	const framed = (c: SceneCamera): SceneCamera => {
		const k = Math.max(1, 1.2 / aspect);
		return { ...c, pos: c.pos.map((v, i) => c.target[i] + (v - c.target[i]) * k) as Vec3 };
	};
	let goal = $derived(framed(mesh ? MESH : focused ? closeUp(focused) : RACK[view]));
	const cam = Tween.of(() => [...goal.pos, ...goal.target, goal.fov ?? 40], { duration: 900, easing: cubicOut });
	let camera = $derived<SceneCamera>({ pos: cam.current.slice(0, 3) as Vec3, target: cam.current.slice(3, 6) as Vec3, fov: cam.current[6] });

	// ── picking ───────────────────────────────────────────────────────
	let selected = $state<string | null>(null);
	let open = $state(false);
	const deviceOf = (id: string) => id.split('/')[0];
	function pick(id: string) {
		const dev = deviceOf(id);
		// In the mesh the first pick focuses the device there; a second
		// takes it to the rack.
		if (mesh) {
			const id = byTag.get(dev)?.id ?? null;
			selected = null;
			if (meshFocus !== id) meshFocus = id;
			else showInRack();
			return;
		}
		if (focus !== dev) {
			// First click on a device zooms to it; the next opens a faceplate.
			focus = dev;
			open = false;
			selected = null;
			return;
		}
		open = true;
	}
	let part = $derived(selected ? partById.get(selected) : undefined);
	let pickedDevice = $derived(selected ? byTag.get(deviceOf(selected)) : undefined);
	let tag = $derived(part ? part.tag : selected && byTag.has(selected) ? selected : undefined);
	let value = $derived(tag ? tags[tag] : undefined);
	let cable = $derived.by(() => {
		if (part?.kind !== 'port' || !pickedDevice) return [];
		const at = linkOnPort(plant, pickedDevice.tag, part.partId, pickedDevice.profile, tags);
		return at ? linkFacts(at.check, at.near, at.link) : [{ label: 'Cable', value: 'none declared' }];
	});
	let portsUp = $derived(
		pickedDevice?.kind === 'switch' ? pickedDevice.parts.filter((p) => p.tag && portReading(tags[p.tag]).up === true).length : undefined
	);
	let legendCtx = $derived<OverlayContext | undefined>(focused ? { node: focused.tag, profile: focused.profile, tags, colors } : undefined);

	function showInRack() {
		const d = devices.find((x) => x.id === meshFocus);
		mesh = false;
		meshFocus = null;
		if (d) focus = d.tag;
	}
	function toMesh() {
		// Keep the context: the device in focus stays in focus in the mesh.
		meshFocus = focused?.id ?? null;
		mesh = true;
		back();
	}
	function back() {
		focus = null;
		open = false;
		selected = null;
	}

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

<svelte:window onresize={() => (aspect = window.innerWidth / window.innerHeight)} onkeydown={(e) => e.key === 'Escape' && !open && (mesh ? (meshFocus = null) : back())} />

<svelte:head><title>HQ rack · 3D</title></svelte:head>

<ReplayClock replay={(rt.frame as { replay?: any } | null)?.replay} />

<div class="stage">
	<SceneView {rt} {alarms} {camera} grid={{ pos: [0, 0, -D / 2], cell: 0.1, section: 0.5, size: [4, 4] }} inspector={false} bind:selected onselect={pick} perf={params.has('perf') || aspect > 1}>
		<Studio />
		{#if mesh}
			<NetworkMesh {topology} {checks} {colors} {summary} labels={edgeLabels} focus={meshFocus ?? undefined} />
		{:else}
		<Rack {layout} label={layout.name} />
		{#each devices as d (d.id)}
			{@const on = focus === d.tag}
			{@const fade = focusFade(topology, focused?.id, d.id)}
			<Server
				profile={d.profile}
				node={d.tag}
				kind={d.kind}
				label={fade > 0.2 ? (d.hostname ?? d.id) : ''}
				pos={placeOf(d).pos}
				{fade}
				rot={[0, d.place.rotY, 0]}
				lid={on && d.kind === 'server' ? lid : 'on'}
				xray={on && xray}
				exploded={on && exploded}
				anchor={false}
				overlay={on ? overlay : undefined}
			/>
		{/each}
		{#if showCables}
			{#each checks as c, i (i)}
				{@const pts = paths[i]}
				{#if pts}
					<Cable points={pts} color={verdictColor(c.check.verdict, colors)} faint={c.check.verdict === 'unverified'} opacity={!hood || hood.links.has(i) ? 1 : 0.08} />
				{/if}
			{/each}
		{/if}
		{/if}
		{#snippet hud()}
			<div class="bar" role="toolbar" aria-label="Views">
				<button class:on={!mesh} onclick={() => (meshFocus ? showInRack() : (mesh = false))}>physical</button>
				<button class:on={mesh} onclick={toMesh}>mesh</button>
				<span class="sep"></span>
				{#if mesh}
					{#if meshFocus}
						<button onclick={() => (meshFocus = null)}>← all</button>
						<b class="where">{topology.devices.find((d) => d.id === meshFocus)?.hostname ?? meshFocus}</b>
						<button onclick={showInRack}>show in rack →</button>
						<span class="sep"></span>
					{/if}
					<button class:on={edgeLabels} onclick={() => (edgeLabels = !edgeLabels)}>port labels</button>
				{:else if focused}
					<button onclick={back}>← rack</button>
					<b class="where">{focused.hostname ?? focused.id}</b>
					{#if focused.kind === 'server'}
						<button class:on={lid === 'on'} onclick={() => (lid = lid === 'on' ? 'off' : 'on')}>lid {lid}</button>
						<button class:on={xray} onclick={() => (xray = !xray)}>x-ray</button>
						<button class:on={exploded} onclick={() => (exploded = !exploded)}>exploded</button>
						<span class="sep"></span>
					{/if}
					{#each overlays.filter((o) => focused?.kind === 'server' || o.id === 'cables' || o.id === 'interfaces') as o}
						<button class:on={overlayId === o.id} onclick={() => (overlayId = overlayId === o.id ? null : o.id)}>{o.name.toLowerCase()}</button>
					{/each}
				{:else}
					{#each Object.keys(RACK) as v}
						<button class:on={view === v} onclick={() => (view = v)}>{v}</button>
					{/each}
					<span class="sep"></span>
					<button class:on={showCables} onclick={() => (showCables = !showCables)}>cables</button>
					<a class="btn" href="/">node1 →</a>
				{/if}
			</div>
		{/snippet}
	</SceneView>
</div>

<aside class="legend" aria-label="Links">
	{#if !mesh && focused && overlay && legendCtx && overlay.id !== 'cables'}
		<b>{overlay.name} · {focused.hostname ?? focused.id}</b>
		<ul>
			{#each overlay.legend(legendCtx) as item}
				<li>
					{#if item.color}<i style:background={item.color}></i>{/if}
					<span>{item.label}</span>
				</li>
			{/each}
		</ul>
		<p>{overlay.caption}</p>
	{:else}
		<b>Cables · {topology.links.length} declared</b>
		<ul>
			{#each ['confirmed', 'consistent', 'contradicted', 'down', 'unverified'] as Verdict[] as v}
				<li class:none={counts[v] === 0}>
					<i style:background={verdictColor(v, colors)}></i>
					<span>{VERDICT_MARK[v]} {v} <em>{counts[v]}</em></span>
				</li>
			{/each}
		</ul>
		<details>
			<summary>every link</summary>
			<ol>
				{#each checks as c}
					<li title={c.check.reasons.join('; ')}>
						<i style:background={verdictColor(c.check.verdict, colors)}></i>
						<button class="link" onclick={() => c.check.a.device && (mesh ? (meshFocus = c.check.a.device.id) : (focus = c.check.a.device.tag))}>
							{VERDICT_MARK[c.check.verdict]} {c.check.a.label} ↔ {c.check.b.label}
						</button>
					</li>
				{/each}
			</ol>
		</details>
		<p>Declared in the site topology, checked live: ✓ an end names the other (LLDP / MAC), = both ends up at the same speed, ✗ the ends disagree, ↓ no end that reports has link, ? an end not reported (faint). Not racked yet: the units are a default (switches on top, nodes below).</p>
	{/if}
</aside>

{#if open && selected && pickedDevice}
	<PartFaceplate
		{part}
		server={{ label: `${pickedDevice.hostname ?? pickedDevice.id} · ${pickedDevice.profile.name}`, tag: pickedDevice.tag, kind: pickedDevice.kind === 'switch' ? 'switch' : 'server', portsUp }}
		{value}
		quality={tag ? rt.quality(tag) : 'good'}
		note={isStubTag(stub, tag) ? `Stub: ${stub.source}.` : undefined}
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
	.where {
		font: 600 13px/1 system-ui, sans-serif;
		margin: 0 6px;
	}
	button,
	.btn {
		font: 12px/1 system-ui, sans-serif;
		padding: 5px 10px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		cursor: pointer;
		text-decoration: none;
	}
	button.on {
		border-color: var(--accent, #6aa5e8);
		color: var(--accent, #6aa5e8);
	}
	.legend {
		position: fixed;
		left: 12px;
		bottom: 12px;
		max-width: 320px;
		max-height: 55vh;
		overflow: auto;
		padding: 10px 12px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.4 system-ui, sans-serif;
	}
	.legend ul,
	.legend ol {
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
	.legend li.none {
		opacity: 0.45;
	}
	.legend em {
		font-style: normal;
		font-family: ui-monospace, monospace;
		color: var(--ink-2, #a8a6a1);
		margin-left: 4px;
	}
	.legend i {
		width: 12px;
		height: 12px;
		border-radius: 3px;
		flex: none;
	}
	.legend .link {
		all: unset;
		cursor: pointer;
		font: 11px/1.3 ui-monospace, monospace;
	}
	.legend .link:hover {
		color: var(--accent, #6aa5e8);
	}
	.legend summary {
		cursor: pointer;
		color: var(--ink-2, #a8a6a1);
	}
	.legend p {
		margin: 6px 0 0;
		color: var(--ink-2, #a8a6a1);
		font-size: 11px;
	}
	@media (max-width: 600px) {
		.legend {
			max-width: none;
			right: 12px;
			max-height: 30vh;
		}
		.legend p {
			display: none;
		}
	}
</style>
