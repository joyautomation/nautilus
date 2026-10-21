<script lang="ts">
	// Renders a scene inside a Threlte <Canvas>: a document, children, or
	// both. Every document node goes through <Node>, which looks its kind
	// up in the registry and wraps it with its label, halo and selection —
	// so a scene is fully described by its JSON file, this component knows
	// no kind by name, and a hand-written <Node> in the children is the
	// same thing (docs/design/spatial-hmi.md §3d). The scene is provided by
	// context, so nothing below is handed a client.
	//
	// Two halves are dynamic imports (§3c): GltfNode, which every data kind
	// renders through, and Surroundings, the HDRI and shadows. A document
	// without a model or an environment never fetches them, and the base
	// bundle holds at its Milestone 1 size.
	import { T, useThrelte } from '@threlte/core';
	import { Grid, OrbitControls, interactivity } from '@threlte/extras';
	import { Color, NoToneMapping } from 'three';
	import { getContext, setContext, type Component, type Snippet } from 'svelte';
	import type { SceneCamera, SceneDoc, SceneEnvironment, SceneGrid, SceneLook, Vec3 } from '../scene.js';
	import { registryFor, type NodeRegistry, type NodeProps, type Box } from '../registry.js';
	import type { AssetAlarm } from '../alarms.js';
	import { SCENE, type PlacedNode, type SceneContext } from '../context.js';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { ViewState } from './view.js';
	import PaperOutlines from './PaperOutlines.svelte';
	import Node from './Node.svelte';
	import Pipe from './Pipe.svelte';
	import Fixture3D from './Fixture3D.svelte';

	let {
		doc,
		registry,
		tags = {},
		isGood = () => true,
		alarms = new Map(),
		selected = null,
		look,
		camera,
		grid,
		environment,
		placed = $bindable({}),
		onpick,
		oncontext,
		children
	}: {
		doc?: SceneDoc;
		registry: NodeRegistry;
		/** The frame's tag map. */
		tags?: Record<string, unknown>;
		isGood?: (tag: string) => boolean;
		/** Worst active alarm per asset (worstAlarmByAsset). */
		alarms?: Map<string, AssetAlarm>;
		selected?: string | null;
		/** `lit`: data kinds, PBR, textures and the environment (§3c).
		 * `flat`: the Milestone 1 look — built-in primitives, three lights.
		 * `paper`: the drawn look (§3f). Absent: the document's, else lit. */
		look?: SceneLook;
		/** Stand-ins for the document's blocks when composing in Svelte. */
		camera?: SceneCamera;
		grid?: SceneGrid;
		environment?: SceneEnvironment;
		/** Every placed node with an id, as they announce themselves — what
		 * the inspector drawer reads. Bind it, or let it be. */
		placed?: Record<string, PlacedNode>;
		/** A node was clicked (not dragged). Never fires for a click on nothing. */
		onpick?: (id: string) => void;
		/** A node was right-clicked; `event` is the browser's (clientX/Y). */
		oncontext?: (id: string, event: MouseEvent) => void;
		/** Svelte authoring: <Node>, <Pipe>, <Fixture3D>, anything Threlte. */
		children?: Snippet;
	} = $props();
	const view = getContext<ViewState>('hmi3d:view');
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let theLook = $derived<SceneLook>(look ?? doc?.look ?? 'lit');
	let paper = $derived(theLook === 'paper');
	$effect(() => {
		if (!view) return;
		view.lit = theLook === 'lit';
		view.paper = paper;
	});

	// Threlte's pointer plugin: every T.Group below with an onclick becomes
	// a raycast target.
	interactivity();

	let cam = $derived(camera ?? doc?.camera ?? { pos: [2, 1.5, 2.5] as Vec3, target: [0, 0, 0] as Vec3 });
	let theGrid = $derived(grid ?? doc?.grid);
	let lit = $derived(theLook === 'lit');
	let env = $derived(lit ? (environment ?? doc?.environment) : undefined);

	// An orthographic camera's frustum is in pixels (Threlte sizes it to
	// the canvas), so its zoom is pixels per metre: the canvas's width over
	// the metres the document wants across it.
	const { canvas, size, scene, renderer, invalidate } = useThrelte();
	let ortho = $derived(cam.projection === 'orthographic');
	let zoom = $derived($size.width / (cam.width ?? 6));

	// Paper: the backdrop is the sheet itself, whatever the page behind the
	// canvas is, and no tone mapping — the filmic curve exists to keep a
	// lit scene's highlights, and would turn this look's whites grey. Both
	// go again with the look.
	$effect(() => {
		if (!paper) return;
		scene.background = new Color(palette.paper);
		const was = renderer.toneMapping;
		renderer.toneMapping = NoToneMapping;
		invalidate();
		return () => {
			scene.background = null;
			renderer.toneMapping = was;
			invalidate();
		};
	});

	// Data kinds: the loader chunk is fetched once a document has one, and
	// until it arrives (or when a model fails) the Svelte kind of the same
	// name renders, so a project with no assets — or a bad path — is grey
	// primitives, never a hole. In the flat and paper looks a model kind
	// that overrides a built-in's name keeps the built-in (the before/after
	// the flat look is for); one under a new name draws its model in that
	// look's materials (GltfNode reads the view), so a station of modelled
	// kinds is never a page of labels over nothing.
	let hasModels = $derived(Object.values(doc?.kinds ?? {}).some((k) => typeof k !== 'string' && k.model));
	let GltfNode = $state<Component<NodeProps> | null>(null);
	$effect(() => {
		if (hasModels && !GltfNode) import('./GltfNode.svelte').then((m) => (GltfNode = m.default as Component<NodeProps>));
	});
	let failed = $state<Record<string, boolean>>({});
	let effective = $derived(doc ? registryFor(doc, registry, GltfNode, lit) : registry);

	// The pickable node under the pointer; Node draws its hover outline.
	// The cursor goes on the canvas itself: OrbitControls writes `cursor:
	// auto` on the canvas's wrapper, which would override one set on body.
	let hovered = $state<string | null>(null);
	$effect(() => () => {
		canvas.style.cursor = '';
	});

	// Bounds a data kind reports once its model is loaded.
	let autoBounds = $state<Record<string, Box>>({});
	const FALLBACK: Box = { size: [0.3, 0.3, 0.3], center: [0, 0.15, 0] };

	setContext<SceneContext>(SCENE, {
		get tags() {
			return tags;
		},
		isGood: (t) => isGood(t),
		get alarms() {
			return alarms;
		},
		get registry() {
			return effective;
		},
		get selected() {
			return selected;
		},
		get hovered() {
			return hovered;
		},
		hover(id) {
			hovered = id;
			// The cursor says a click will do something.
			canvas.style.cursor = id ? 'pointer' : '';
		},
		defFor(id, kind) {
			const d = effective[kind];
			return d?.data && id !== undefined && failed[id] ? registry[kind] : d;
		},
		boundsOf: (id, b) => (b === 'auto' ? (id !== undefined ? (autoBounds[id] ?? FALLBACK) : FALLBACK) : b),
		reportBounds(id, box) {
			if (id !== undefined) autoBounds[id] = box;
		},
		reportFailed(id) {
			if (id !== undefined) failed[id] = true;
		},
		pick: (id) => onpick?.(id),
		contextPick: (id, e) => oncontext?.(id, e),
		register(node) {
			placed[node.id] = node;
			return () => {
				delete placed[node.id];
			};
		}
	});
</script>

{#if ortho}
	<T.OrthographicCamera makeDefault position={cam.pos} {zoom} near={-100} far={100}>
		<OrbitControls target={cam.target} enableDamping />
	</T.OrthographicCamera>
{:else}
	<T.PerspectiveCamera makeDefault position={cam.pos} fov={cam.fov ?? 45} near={0.01} far={100}>
		<OrbitControls target={cam.target} enableDamping />
	</T.PerspectiveCamera>
{/if}

{#if env}
	{#await import('./Surroundings.svelte') then m}
		<m.default {env} />
	{/await}
{:else if paper}
	<!-- Nearly flat: the lines carry the form, the light only tells one
	     face from the next. (Physical units: an ambient of π lights a
	     white face to white; the key adds a few percent by its angle.) -->
	<T.AmbientLight intensity={2.6} />
	<T.DirectionalLight position={[3, 5, 4]} intensity={0.8} />
	<PaperOutlines color={palette.ink} />
{:else}
	<T.AmbientLight intensity={0.6} />
	<T.DirectionalLight position={[3, 5, 4]} intensity={1.6} />
{/if}

{#if theGrid}
	<Grid
		position={theGrid.pos ?? [0, 0, 0]}
		cellSize={theGrid.cell ?? 0.1}
		sectionSize={theGrid.section ?? 1}
		gridSize={theGrid.size ?? [4, 4]}
		cellColor={paper ? '#e6e6e2' : '#2c2c2a'}
		sectionColor={paper ? '#d2d2cd' : '#44443f'}
		fadeDistance={paper ? 40 : 12}
	/>
{/if}

{#if doc}
	{#each doc.fixtures ?? [] as f, i (i)}
		<Fixture3D fixture={f} />
	{/each}

	{#each doc.pipes ?? [] as p, i (p.id ?? i)}
		<Pipe points={p.points} radius={p.radius} flowing={p.bind?.flowing} arrows={p.arrows} />
	{/each}

	{#each doc.nodes as n (n.id)}
		<Node {...n} />
	{/each}
{/if}

{@render children?.()}
