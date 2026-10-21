<script lang="ts">
	// The kind editor's stage: the renderer's <Canvas> with ONE node of the
	// kind at the origin, read from a document the stage composes — the
	// kind under its own name, a node bound to a sample struct — so what is
	// on stage is exactly what a scene that places the kind would draw. The
	// model is also loaded here, once per URI, for what the panel needs
	// from it: the named objects a drive can target, and its box, which
	// frames the camera.
	import { Canvas } from '@threlte/core';
	import { onMount, setContext, type Component } from 'svelte';
	import { Box3, Vector3 } from 'three';
	import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js';
	import {
		builtinRegistry,
		validateKind,
		DEFAULT_PALETTE,
		paletteFromTheme,
		paperPalette,
		type NodeProps,
		type NodeRegistry,
		type Palette,
		type SceneDoc,
		type SceneError
	} from '@joyautomation/nautilus-hmi-3d';
	import PlaceholderKind from './PlaceholderKind.svelte';
	import KindInner from './KindInner.svelte';
	import { kd, sampleStruct, type Box } from './kindState.svelte';

	type Vec3 = [number, number, number];
	const FALLBACK: Box = { size: [0.4, 0.4, 0.4], center: [0, 0.2, 0] };

	const palette = $state<Palette>(structuredClone(DEFAULT_PALETTE));
	setContext('hmi3d:palette', palette);
	const view = $state({ lit: true, paper: false });
	setContext('hmi3d:view', view);

	function placeholderFor(box: Box): Component<NodeProps> {
		return ((anchor: unknown, props: unknown) => (PlaceholderKind as unknown as (a: unknown, p: unknown) => unknown)(anchor, { ...(props as object), box })) as unknown as Component<NodeProps>;
	}

	// Load the model for its names and its box. The renderer loads it
	// again through its own cache; two fetches of one small file, and the
	// panel's list never waits on a frame.
	let loader: GLTFLoader | undefined;
	$effect(() => {
		const url = kd.rendered?.model;
		if (!url || (kd.model && !kd.model.file)) {
			kd.meshes = [];
			kd.bounds = null;
			kd.modelState = 'none';
			return;
		}
		let cancelled = false;
		kd.modelState = 'loading';
		loader ??= new GLTFLoader();
		loader.loadAsync(url).then(
			(gltf) => {
				if (cancelled) return;
				const names: string[] = [];
				gltf.scene.traverse((o) => {
					if (o.name && o !== gltf.scene) names.push(o.name);
				});
				gltf.scene.updateMatrixWorld(true);
				const box = new Box3().setFromObject(gltf.scene);
				const size = new Vector3();
				const center = new Vector3();
				box.getSize(size);
				box.getCenter(center);
				kd.meshes = names;
				kd.bounds = { size: size.toArray() as Vec3, center: center.toArray() as Vec3 };
				kd.modelState = 'ok';
			},
			(err) => {
				if (cancelled) return;
				console.warn('nautilus kind: could not load the model', err);
				kd.meshes = [];
				kd.bounds = null;
				kd.modelState = 'failed';
			}
		);
		return () => {
			cancelled = true;
		};
	});

	/** The box the stage frames: the model's, else the declared bounds, else a hand's width. */
	const box = $derived<Box>(kd.bounds ?? (kd.kind?.bounds && kd.kind.bounds !== 'auto' ? kd.kind.bounds : FALLBACK));

	// The camera: three-quarter view, far enough to take the whole box.
	const camera = $derived.by(() => {
		const r = Math.max(box.size[0], box.size[1], box.size[2], 0.2);
		const c = box.center;
		const d = r * 1.9;
		return { pos: [c[0] + d * 0.8, c[1] + d * 0.55, c[2] + d * 0.9] as Vec3, target: c };
	});
	const grid = $derived({ pos: [0, Math.min(0, box.center[1] - box.size[1] / 2), 0] as Vec3, size: [4, 4] as [number, number], cell: 0.1, section: 1 });

	// The document on stage: the kind under its name, one node bound to
	// the sample struct. The kind is the RENDERED one (model as a URI).
	const doc = $derived.by((): SceneDoc | null => {
		const k = kd.rendered;
		if (!k) return null;
		const usable = kd.model && !kd.model.file ? { ...k, model: undefined, drive: undefined } : k;
		return {
			name: kd.name,
			look: kd.look,
			kinds: { [kd.name]: usable },
			camera,
			grid,
			nodes: [{ id: 'PREVIEW', kind: kd.name, tag: 'PREVIEW', label: kd.name, pos: [0, 0, 0] }]
		};
	});
	const registry = $derived.by((): NodeRegistry => {
		const reg: NodeRegistry = { ...builtinRegistry };
		const k = kd.kind;
		// A kind under a built-in's name keeps the built-in; any other name
		// gets a placeholder for when there is no model (or it fails).
		if (!reg[kd.name]) reg[kd.name] = { component: placeholderFor(box), bounds: box, type: k?.type, members: k?.members, labelAt: k?.labelAt };
		return reg;
	});
	const tags = $derived<Record<string, unknown>>({ PREVIEW: sampleStruct() });

	const errors = $derived<SceneError[]>(kd.kind ? validateKind(kd.kind).errors : []);
	/** Drives whose mesh the loaded model does not have — the warning
	 * hmi-3d logs, said where it can be fixed. */
	const missingMeshes = $derived(
		kd.modelState === 'ok' ? [...new Set((kd.kind?.drive ?? []).map((d) => d.mesh).filter((m) => !kd.meshes.includes(m)))] : []
	);

	let stage: HTMLElement;
	let webgl = $state(true);
	let themed = $state<Palette | null>(null);
	$effect(() => {
		if (themed) Object.assign(palette, kd.look === 'paper' ? paperPalette(themed) : themed);
	});
	onMount(() => {
		themed = paletteFromTheme(stage);
		try {
			const c = document.createElement('canvas');
			webgl = !!(c.getContext('webgl2') ?? c.getContext('webgl'));
		} catch {
			webgl = false;
		}
	});
</script>

<div class="stage" bind:this={stage}>
	{#if errors.length || missingMeshes.length}
		<div class="errors" role="alert">
			{#if errors.length}
				<b>{errors.length} problem{errors.length === 1 ? '' : 's'} in the kind</b>
				<ul>
					{#each errors as e (e.path + e.message)}
						<li><code>{e.path || '/'}</code> {e.message}</li>
					{/each}
				</ul>
			{/if}
			{#if missingMeshes.length}
				<b>The model has no object named {missingMeshes.join(', ')}</b> — those drives do nothing. It has: {kd.meshes.join(', ') || 'nothing named'}.
			{/if}
		</div>
	{/if}
	{#if !webgl}
		<div class="nogl" role="alert">
			<b>This window has no WebGL, so the kind cannot be drawn here.</b>
			<p>The file is still editable as text: right-click it and choose <i>Open With → Text Editor</i>, or reopen it from a window with graphics acceleration.</p>
		</div>
	{:else if doc && errors.length === 0}
		<Canvas>
			<KindInner {doc} {registry} {tags} />
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
	.errors ul {
		margin: 4px 0 0;
		padding-left: 18px;
	}
	.errors code {
		font-family: var(--nx-mono);
		color: var(--nx-muted);
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
</style>
