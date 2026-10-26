<script lang="ts">
	// The whole 3D view on one page: validates the document, reads the theme
	// into a palette, renders the scene in a <Canvas>, shows the HUD and the
	// click-to-inspect drawer. The page owns the realtime and alarm clients
	// (start/stop them as the kit's other components expect) and hands them
	// in; this component reads them. A scene is a document, children
	// (docs/design/spatial-hmi.md §3d: <Node>, <Pipe>, anything Threlte), or
	// both.
	import { onMount, setContext, type Snippet } from 'svelte';
	import { Canvas } from '@threlte/core';
	import type { RealtimeClient, AlarmClient, NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { readPath, resolveRefs } from '../bindings.js';
	import { validateScene, refRoot, type SceneCamera, type SceneDoc, type SceneEnvironment, type SceneError, type SceneGrid, type SceneLook } from '../scene.js';
	import { builtinRegistry, componentRegistry, inlineKindFiles, type NodeRegistry } from '../registry.js';
	import { worstAlarmByAsset, alarmAsset } from '../alarms.js';
	import { DEFAULT_PALETTE, paletteFromTheme, paperPalette, type Palette } from '../palette.js';
	import { PerfSampler } from '../perf.svelte.js';
	import type { PlacedNode } from '../context.js';
	import type { ViewState } from './view.js';
	import Scene3D from './Scene3D.svelte';
	import AssetDrawer from './AssetDrawer.svelte';
	import PerfHud from './PerfHud.svelte';

	let {
		doc,
		rt,
		alarms = null,
		registry = builtinRegistry,
		modules,
		camera,
		grid,
		environment,
		perf = false,
		inspector = true,
		look = $bindable(),
		selected = $bindable(null),
		onselect,
		oncontext,
		hud,
		children
	}: {
		doc?: SceneDoc;
		rt: RealtimeClient<NautilusFrame>;
		alarms?: AlarmClient | null;
		registry?: NodeRegistry;
		/** The app's Svelte modules, `import.meta.glob of the app's .svelte files, eager`,
		 * for a document whose `kinds` name component files (§3d). */
		modules?: Record<string, unknown>;
		/** Stand-ins for the document's blocks when composing in Svelte. */
		camera?: SceneCamera;
		grid?: SceneGrid;
		environment?: SceneEnvironment;
		/** Show the fps / latency HUD. */
		perf?: boolean;
		/** Open the asset drawer on a pick. */
		inspector?: boolean;
		/** `lit`: data kinds, PBR materials, textures and the document's
		 * environment. `flat`: the Milestone 1 look — built-in primitives
		 * and three lights — for a project with no assets, a low-end tier,
		 * or the before half of a before/after. `paper`: the drawn look
		 * (§3f): near-white fills, inked edges, colour only on an alarm.
		 * Absent: the document's `look`, else `lit`. */
		look?: SceneLook;
		/** The picked node's id. Bind it to follow the selection; Escape
		 * clears it (and closes the drawer). */
		selected?: string | null;
		/** A node was picked. Not called on Escape — a bound `selected` goes null. */
		onselect?: (id: string) => void;
		/** A node was right-clicked (a context menu's cue); `event` is the
		 * browser's, for clientX / clientY. Picks nothing. */
		oncontext?: (id: string, event: MouseEvent) => void;
		/** Extra HUD content, rendered after the built-in strip. */
		hud?: Snippet;
		/** Svelte authoring: <Node>, <Pipe>, <Fixture3D>, anything Threlte. */
		children?: Snippet;
	} = $props();

	// The palette is context, so every model reads the same theme colours.
	// It starts as the defaults and is re-read from the DOM once mounted;
	// the paper look lays its near-whites over whatever the theme gave.
	let palette = $state<Palette>(DEFAULT_PALETTE);
	setContext('hmi3d:palette', palette);
	let themed = $state<Palette>(DEFAULT_PALETTE);
	let theLook = $derived<SceneLook>(look ?? doc?.look ?? 'lit');
	$effect(() => {
		Object.assign(palette, theLook === 'paper' ? paperPalette(themed) : themed);
	});
	// The look, read by every model for its material values (view.ts).
	const view = $state<ViewState>({ lit: true, paper: false });
	setContext('hmi3d:view', view);

	// Kind files (§3g) are inlined from the app's modules first, then
	// component kinds (§3d) join the registry from the same modules; their
	// errors — a path no module matches, a member the export reads that the
	// document does not list — are listed with the document's.
	let inlined = $derived(doc && modules ? inlineKindFiles(doc, modules) : { doc, errors: [] as SceneError[] });
	let shown = $derived(inlined.doc);
	let withComponents = $derived(shown && modules ? componentRegistry(shown, modules, registry) : { registry, errors: [] as SceneError[] });
	let check = $derived.by(() => {
		const errors = shown ? validateScene(shown, Object.keys(withComponents.registry)).errors : [];
		errors.push(...inlined.errors, ...withComponents.errors);
		return { ok: errors.length === 0, errors };
	});
	let tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	let byAsset = $derived(worstAlarmByAsset(alarms?.instances ?? []));

	let drawerOpen = $state(false);
	// Every placed node with an id — document nodes and hand-written ones
	// alike — announces itself here, which is what the drawer reads.
	let placed = $state<Record<string, PlacedNode>>({});
	let node = $derived(selected ? placed[selected] : undefined);
	let nodeAlarms = $derived(
		node?.tag ? (alarms?.instances ?? []).filter((a) => a.state !== 'normal' && alarmAsset(a.tag) === node!.tag) : []
	);
	// What the drawer shows: the struct under the node's tag, or, for a
	// flat-tag node, its bound refs resolved into one object under the
	// member names (the same map the geometry reads). Quality is the
	// tag's, or the worst of the refs' root tags.
	let nodeValue = $derived(node?.tag ? tags[node.tag] : node?.bind ? resolveRefs(node.bind, (p) => readPath(tags, p)) : undefined);
	let nodeQuality = $derived.by(() => {
		if (!node) return 'good';
		if (node.tag) return rt.quality(node.tag);
		for (const ref of Object.values(node.bind ?? {})) {
			const q = rt.quality(refRoot(ref));
			if (q !== 'good') return q;
		}
		return 'good';
	});

	function pick(id: string) {
		selected = id;
		if (inspector) drawerOpen = true;
		onselect?.(id);
	}
	// Escape deselects, wherever the focus is: the cage goes, the drawer
	// closes (the kit's Drawer closes itself on Escape too, as a dialog).
	function onkey(e: KeyboardEvent) {
		if (e.key !== 'Escape' || selected === null) return;
		selected = null;
		drawerOpen = false;
	}

	const sampler = new PerfSampler();
	let stage: HTMLElement;
	onMount(() => {
		themed = paletteFromTheme(stage);
		if (!perf) return;
		sampler.start();
		const off = rt.onFrame((f) => sampler.onFrame(f.ts));
		return () => {
			off();
			sampler.stop();
		};
	});
</script>

<svelte:window onkeydown={onkey} />

<div class="stage" bind:this={stage}>
	{#if !check.ok}
		<div class="errors" role="alert">
			<b>{doc?.name ?? 'Scene'}: the scene has {check.errors.length} error{check.errors.length === 1 ? '' : 's'}</b>
			<ul>
				{#each check.errors as e}
					<li><code>{e.path || '/'}</code> {e.message}</li>
				{/each}
			</ul>
		</div>
	{:else}
		<Canvas>
			<Scene3D
				doc={shown}
				registry={withComponents.registry}
				{tags}
				isGood={(t) => rt.isGood(t)}
				alarms={byAsset}
				{selected}
				look={theLook}
				{camera}
				{grid}
				{environment}
				bind:placed
				onpick={pick}
				{oncontext}
			>
				{@render children?.()}
			</Scene3D>
		</Canvas>
	{/if}

	<div class="hud-slot">
		{#if perf}
			<PerfHud {sampler} connected={rt.connected} active={alarms?.summary?.active ?? 0} label={doc?.name ?? 'controller'} />
		{/if}
		{@render hud?.()}
	</div>
</div>

{#if inspector}
	<AssetDrawer
		bind:open={drawerOpen}
		{node}
		def={node?.kind ? withComponents.registry[node.kind] : undefined}
		value={nodeValue}
		quality={nodeQuality}
		alarms={nodeAlarms}
	/>
{/if}

<style>
	.stage {
		position: relative;
		width: 100%;
		height: 100%;
		min-height: 240px;
	}
	.hud-slot {
		position: absolute;
		top: 12px;
		left: 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		pointer-events: none;
	}
	.hud-slot > :global(*) {
		pointer-events: auto;
	}
	.errors {
		padding: 16px;
		font: 14px system-ui, sans-serif;
		color: var(--ink, #e8e6e1);
	}
	.errors code {
		font-family: ui-monospace, monospace;
		color: var(--crit, #d03b3b);
		margin-right: 6px;
	}
</style>
