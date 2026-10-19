<script lang="ts">
	// The stage: the renderer's <Canvas>, with the palette and view contexts
	// SceneView would otherwise provide (the editor uses Scene3D directly —
	// it wants no HUD and no inspector drawer, and owns the selection). The
	// registry is the kit's built-ins plus a placeholder box for every kind
	// of the document's that this bundle cannot draw (PlaceholderKind), so
	// the whole document is on stage whatever its kinds are made of.
	import { Canvas } from '@threlte/core';
	import { onMount, setContext, type Component } from 'svelte';
	import {
		builtinRegistry,
		validateScene,
		DEFAULT_PALETTE,
		paletteFromTheme,
		type NodeProps,
		type NodeRegistry,
		type Palette,
		type SceneDoc,
		type SceneError
	} from '@joyautomation/nautilus-hmi-3d';
	import PlaceholderKind from './PlaceholderKind.svelte';
	import SceneInner from './SceneInner.svelte';
	import { ed } from './sceneState.svelte';

	type Vec3 = [number, number, number];
	type Box = { size: Vec3; center: Vec3 };
	const FALLBACK: Box = { size: [0.4, 0.4, 0.4], center: [0, 0.2, 0] };

	const palette = $state<Palette>(structuredClone(DEFAULT_PALETTE));
	setContext('hmi3d:palette', palette);
	// Scene3D writes `lit` from its look prop; models read it for materials.
	const view = $state({ lit: true });
	setContext('hmi3d:view', view);

	/** A placeholder component drawing `box`: Svelte 5 components are
	 * functions of (anchor, props), so one per kind is a closure. */
	function placeholderFor(box: Box): Component<NodeProps> {
		return ((anchor: unknown, props: unknown) => (PlaceholderKind as unknown as (a: unknown, p: unknown) => unknown)(anchor, { ...(props as object), box })) as unknown as Component<NodeProps>;
	}

	// Which document the stage draws: the one with asset URIs the webview
	// can fetch (ed.rendered); ed.doc is the text as written.
	const doc = $derived(ed.rendered);

	const registry = $derived.by((): NodeRegistry => {
		const reg: NodeRegistry = { ...builtinRegistry };
		for (const [name, k] of Object.entries(doc?.kinds ?? {})) {
			// A model or an assembly is laid on by Scene3D itself (registryFor);
			// a re-pointed built-in keeps the built-in's own component. What
			// needs a stand-in is a kind with no Svelte component here: a
			// component kind, a declared one, and a model kind's fallback for
			// when its file does not load.
			if (reg[name] && !k.component) continue;
			if (k.assembly) continue;
			const box = k.bounds && k.bounds !== 'auto' ? k.bounds : FALLBACK;
			reg[name] = { component: placeholderFor(box), bounds: box, type: k.type, members: k.members, labelAt: k.labelAt };
		}
		return reg;
	});

	const errors = $derived<SceneError[]>(doc ? validateScene(doc, Object.keys(registry)).errors : []);
	// The renderer refuses an invalid document (a node of a kind nobody
	// defines, a pipe of one point). The editor still shows what it can:
	// the errors, and the stage with the offending entries filtered out.
	const shown = $derived.by((): SceneDoc | null => {
		if (!doc) return null;
		if (errors.length === 0) return doc;
		const bad = new Set(errors.map((e) => e.path.split('/').slice(0, 3).join('/')));
		return {
			...doc,
			nodes: (doc.nodes ?? []).filter((_, i) => !bad.has(`/nodes/${i}`)),
			pipes: (doc.pipes ?? []).filter((_, i) => !bad.has(`/pipes/${i}`)),
			fixtures: (doc.fixtures ?? []).filter((_, i) => !bad.has(`/fixtures/${i}`))
		};
	});

	let stage: HTMLElement;
	// A window with no WebGL (a remote desktop, a VM without a GPU and
	// without software GL) would make the renderer throw inside <Canvas>;
	// say so instead, with the text editor a click away.
	let webgl = $state(true);
	onMount(() => {
		Object.assign(palette, paletteFromTheme(stage));
		try {
			const c = document.createElement('canvas');
			webgl = !!(c.getContext('webgl2') ?? c.getContext('webgl'));
		} catch {
			webgl = false;
		}
	});
</script>

<div class="stage" bind:this={stage} class:placing={ed.tool === 'place'}>
	{#if errors.length}
		<div class="errors" role="alert">
			<b>{errors.length} problem{errors.length === 1 ? '' : 's'} in the document</b>
			<ul>
				{#each errors as e (e.path + e.message)}
					<li><code>{e.path || '/'}</code> {e.message}</li>
				{/each}
			</ul>
		</div>
	{/if}
	{#if !webgl}
		<div class="nogl" role="alert">
			<b>This window has no WebGL, so the scene cannot be drawn here.</b>
			<p>The document is still editable as text: right-click the file and choose <i>Open With → Text Editor</i>, or reopen it from a window with graphics acceleration.</p>
		</div>
	{:else if shown}
		<Canvas>
			<SceneInner doc={shown} {registry} />
		</Canvas>
	{/if}
</div>

<style>
	.stage {
		position: relative;
		flex: 1;
		min-width: 0;
		min-height: 0;
		background: var(--nx-bg);
	}
	.stage.placing :global(canvas) {
		cursor: crosshair !important;
	}
	.errors {
		position: absolute;
		top: 8px;
		left: 8px;
		right: 8px;
		z-index: 2;
		padding: 8px 10px;
		border: 1px solid var(--nx-err);
		border-radius: 6px;
		background: var(--nx-err-bg);
		color: var(--nx-ui-ink);
		font-size: 12px;
		max-height: 40%;
		overflow: auto;
	}
	.nogl {
		padding: 24px;
		color: var(--nx-ui-ink);
		font-size: 13px;
		max-width: 46em;
	}
	.nogl p {
		color: var(--nx-muted);
	}
	.errors ul {
		margin: 4px 0 0;
		padding-left: 18px;
	}
	.errors code {
		font-family: var(--nx-mono);
		color: var(--nx-muted);
	}
</style>
