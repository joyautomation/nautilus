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
	import { T } from '@threlte/core';
	import { Grid, OrbitControls, interactivity } from '@threlte/extras';
	import { getContext, setContext, type Component, type Snippet } from 'svelte';
	import type { SceneCamera, SceneDoc, SceneEnvironment, SceneGrid, Vec3 } from '../scene.js';
	import { registryFor, type NodeRegistry, type NodeProps, type Box } from '../registry.js';
	import type { AssetAlarm } from '../alarms.js';
	import { SCENE, type PlacedNode, type SceneContext } from '../context.js';
	import type { ViewState } from './view.js';
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
		look = 'lit',
		camera,
		grid,
		environment,
		placed = $bindable({}),
		onpick,
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
		 * `flat`: the Milestone 1 look — built-in primitives, three lights. */
		look?: 'lit' | 'flat';
		/** Stand-ins for the document's blocks when composing in Svelte. */
		camera?: SceneCamera;
		grid?: SceneGrid;
		environment?: SceneEnvironment;
		/** Every placed node with an id, as they announce themselves — what
		 * the inspector drawer reads. Bind it, or let it be. */
		placed?: Record<string, PlacedNode>;
		/** A node was clicked (not dragged). Never fires for a click on nothing. */
		onpick?: (id: string) => void;
		/** Svelte authoring: <Node>, <Pipe>, <Fixture3D>, anything Threlte. */
		children?: Snippet;
	} = $props();
	const view = getContext<ViewState>('hmi3d:view');
	$effect(() => {
		if (view) view.lit = look === 'lit';
	});

	// Threlte's pointer plugin: every T.Group below with an onclick becomes
	// a raycast target.
	interactivity();

	let cam = $derived(camera ?? doc?.camera ?? { pos: [2, 1.5, 2.5] as Vec3, target: [0, 0, 0] as Vec3 });
	let theGrid = $derived(grid ?? doc?.grid);
	let lit = $derived(look === 'lit');
	let env = $derived(lit ? (environment ?? doc?.environment) : undefined);

	// Data kinds: the loader chunk is fetched once a lit document has one,
	// and until it arrives (or when a model fails) the Svelte kind of the
	// same name renders, so a project with no assets — or a bad path — is
	// grey primitives, never a hole.
	let hasModels = $derived(Object.values(doc?.kinds ?? {}).some((k) => k.model));
	let GltfNode = $state<Component<NodeProps> | null>(null);
	$effect(() => {
		if (lit && hasModels && !GltfNode) import('./GltfNode.svelte').then((m) => (GltfNode = m.default as Component<NodeProps>));
	});
	let failed = $state<Record<string, boolean>>({});
	let effective = $derived(doc ? registryFor(doc, registry, lit ? GltfNode : null) : registry);

	// The pickable node under the pointer; Node draws its hover outline.
	let hovered = $state<string | null>(null);
	$effect(() => () => {
		if (typeof document !== 'undefined') document.body.style.cursor = '';
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
			if (typeof document !== 'undefined') document.body.style.cursor = id ? 'pointer' : '';
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
		register(node) {
			placed[node.id] = node;
			return () => {
				delete placed[node.id];
			};
		}
	});
</script>

<T.PerspectiveCamera makeDefault position={cam.pos} fov={cam.fov ?? 45} near={0.01} far={100}>
	<OrbitControls target={cam.target} enableDamping />
</T.PerspectiveCamera>

{#if env}
	{#await import('./Surroundings.svelte') then m}
		<m.default {env} />
	{/await}
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
		cellColor="#2c2c2a"
		sectionColor="#44443f"
		fadeDistance={12}
	/>
{/if}

{#if doc}
	{#each doc.fixtures ?? [] as f, i (i)}
		<Fixture3D fixture={f} />
	{/each}

	{#each doc.pipes ?? [] as p, i (p.id ?? i)}
		<Pipe points={p.points} radius={p.radius} flowing={p.bind?.flowing} />
	{/each}

	{#each doc.nodes as n (n.id)}
		<Node {...n} />
	{/each}
{/if}

{@render children?.()}
