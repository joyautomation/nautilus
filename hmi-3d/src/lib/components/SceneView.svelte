<script lang="ts">
	// The whole 3D view on one page: validates the document, reads the theme
	// into a palette, renders the scene in a <Canvas>, shows the HUD and the
	// click-to-inspect drawer. The page owns the realtime and alarm clients
	// (start/stop them as the kit's other components expect) and hands them
	// in; this component reads them.
	import { onMount, setContext } from 'svelte';
	import { Canvas } from '@threlte/core';
	import type { RealtimeClient, AlarmClient, NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { validateScene, type SceneDoc } from '../scene.js';
	import { builtinRegistry, type NodeRegistry } from '../registry.js';
	import { worstAlarmByAsset, alarmAsset } from '../alarms.js';
	import { DEFAULT_PALETTE, paletteFromTheme, type Palette } from '../palette.js';
	import { PerfSampler } from '../perf.svelte.js';
	import type { ViewState } from './view.js';
	import Scene3D from './Scene3D.svelte';
	import AssetDrawer from './AssetDrawer.svelte';
	import PerfHud from './PerfHud.svelte';

	let {
		doc,
		rt,
		alarms = null,
		registry = builtinRegistry,
		perf = false,
		inspector = true,
		look = $bindable('lit'),
		selected = $bindable(null),
		onselect,
		hud
	}: {
		doc: SceneDoc;
		rt: RealtimeClient<NautilusFrame>;
		alarms?: AlarmClient | null;
		registry?: NodeRegistry;
		/** Show the fps / latency HUD. */
		perf?: boolean;
		/** Open the asset drawer on a pick. */
		inspector?: boolean;
		/** `lit` (default): data kinds, PBR materials, textures and the
		 * document's environment. `flat`: the Milestone 1 look — built-in
		 * primitives and three lights — for a project with no assets, a
		 * low-end tier, or the before half of a before/after. */
		look?: 'lit' | 'flat';
		selected?: string | null;
		onselect?: (id: string) => void;
		/** Extra HUD content, rendered after the built-in strip. */
		hud?: import('svelte').Snippet;
	} = $props();

	// The palette is context, so every model reads the same theme colours.
	// It starts as the defaults and is re-read from the DOM once mounted.
	let palette = $state<Palette>(DEFAULT_PALETTE);
	setContext('hmi3d:palette', palette);
	// The look, read by every model for its material values (view.ts).
	const view = $state<ViewState>({ lit: true });
	setContext('hmi3d:view', view);

	let check = $derived(validateScene(doc, Object.keys(registry)));
	let tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	let byAsset = $derived(worstAlarmByAsset(alarms?.instances ?? []));

	let drawerOpen = $state(false);
	let node = $derived(doc.nodes.find((n) => n.id === selected));
	let nodeAlarms = $derived(
		node?.tag ? (alarms?.instances ?? []).filter((a) => a.state !== 'normal' && alarmAsset(a.tag) === node!.tag) : []
	);

	function pick(id: string) {
		selected = id;
		if (inspector) drawerOpen = true;
		onselect?.(id);
	}

	const sampler = new PerfSampler();
	let stage: HTMLElement;
	onMount(() => {
		Object.assign(palette, paletteFromTheme(stage));
		if (!perf) return;
		sampler.start();
		const off = rt.onFrame((f) => sampler.onFrame(f.ts));
		return () => {
			off();
			sampler.stop();
		};
	});
</script>

<div class="stage" bind:this={stage}>
	{#if !check.ok}
		<div class="errors" role="alert">
			<b>{doc.name ?? 'Scene'}: the scene document has {check.errors.length} error{check.errors.length === 1 ? '' : 's'}</b>
			<ul>
				{#each check.errors as e}
					<li><code>{e.path || '/'}</code> {e.message}</li>
				{/each}
			</ul>
		</div>
	{:else}
		<Canvas>
			<Scene3D {doc} {registry} {tags} isGood={(t) => rt.isGood(t)} alarms={byAsset} {selected} {look} onpick={pick} />
		</Canvas>
	{/if}

	<div class="hud-slot">
		{#if perf}
			<PerfHud {sampler} connected={rt.connected} active={alarms?.summary?.active ?? 0} label={doc.name ?? 'controller'} />
		{/if}
		{@render hud?.()}
	</div>
</div>

{#if inspector}
	<AssetDrawer
		bind:open={drawerOpen}
		{node}
		def={node ? registry[node.kind] : undefined}
		value={node?.tag ? tags[node.tag] : undefined}
		quality={node?.tag ? rt.quality(node.tag) : 'good'}
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
