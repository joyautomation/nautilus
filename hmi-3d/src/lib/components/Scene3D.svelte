<script lang="ts">
	// Renders a scene document inside a Threlte <Canvas>. Every node is
	// looked up in the registry, handed its struct tag and its resolved
	// bindings, and wrapped with its label, its alarm halo and a selection
	// outline — so a scene is fully described by its JSON file and this
	// component knows no kind by name.
	//
	// Two halves are dynamic imports (docs/design/spatial-hmi.md §3c):
	// GltfNode, which every data kind renders through, and Surroundings,
	// the HDRI and shadows. A document without a model or an environment
	// never fetches them, and the base bundle holds at its Milestone 1 size.
	import { T } from '@threlte/core';
	import { Grid, OrbitControls, interactivity, type IntersectionEvent } from '@threlte/extras';
	import type { Component } from 'svelte';
	import { resolveNodeBindings, bindingsGood, readPath, flowing } from '../bindings.js';
	import type { SceneDoc, Vec3 } from '../scene.js';
	import { registryFor, type NodeRegistry, type NodeProps } from '../registry.js';
	import type { AssetAlarm } from '../alarms.js';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { ViewState } from './view.js';
	import Pipe3D from './Pipe3D.svelte';
	import Halo from './Halo.svelte';
	import Label from './Label.svelte';
	import Fixture3D from './Fixture3D.svelte';

	let {
		doc,
		registry,
		tags = {},
		isGood = () => true,
		alarms = new Map(),
		selected = null,
		look = 'lit',
		onpick
	}: {
		doc: SceneDoc;
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
		/** A node was clicked (not dragged). Never fires for a click on nothing. */
		onpick?: (id: string) => void;
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState>('hmi3d:view');
	$effect(() => {
		if (view) view.lit = look === 'lit';
	});

	// Threlte's pointer plugin: every T.Group below with an onclick becomes
	// a raycast target. A click whose pointer travelled more than 5 px since
	// pointerdown is an orbit, not a pick.
	interactivity();
	const DRAG_PX = 5;

	const deg = Math.PI / 180;
	const rad = (r?: Vec3): Vec3 => (r ? [r[0] * deg, r[1] * deg, r[2] * deg] : [0, 0, 0]);

	let cam = $derived(doc.camera ?? { pos: [2, 1.5, 2.5] as Vec3, target: [0, 0, 0] as Vec3 });
	let grid = $derived(doc.grid);
	let lit = $derived(look === 'lit');
	let env = $derived(lit ? doc.environment : undefined);

	// Data kinds: the loader chunk is fetched once a lit document has one,
	// and until it arrives (or when a model fails) the Svelte kind of the
	// same name renders, so a project with no assets — or a bad path — is
	// grey primitives, never a hole.
	let hasModels = $derived(Object.values(doc.kinds ?? {}).some((k) => k.model));
	let GltfNode = $state<Component<NodeProps> | null>(null);
	$effect(() => {
		if (lit && hasModels && !GltfNode) import('./GltfNode.svelte').then((m) => (GltfNode = m.default as Component<NodeProps>));
	});
	let failed = $state<Record<string, boolean>>({});
	let effective = $derived(lit && GltfNode ? registryFor(doc, registry, GltfNode) : registry);
	function defFor(id: string, kind: string) {
		const d = effective[kind];
		return d?.data && failed[id] ? registry[kind] : d;
	}

	// Bounds a data kind reports once its model is loaded.
	let autoBounds = $state<Record<string, { size: Vec3; center: Vec3 }>>({});
	const FALLBACK = { size: [0.3, 0.3, 0.3] as Vec3, center: [0, 0.15, 0] as Vec3 };
	const boundsOf = (id: string, b: { size: Vec3; center: Vec3 } | 'auto') => (b === 'auto' ? (autoBounds[id] ?? FALLBACK) : b);
	const labelOf = (at: Vec3 | undefined, b: { size: Vec3; center: Vec3 }): Vec3 =>
		at ?? [b.center[0], b.center[1] + b.size[1] / 2 + 0.02, b.center[2]];
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

{#if grid}
	<Grid
		position={grid.pos ?? [0, 0, 0]}
		cellSize={grid.cell ?? 0.1}
		sectionSize={grid.section ?? 1}
		gridSize={grid.size ?? [4, 4]}
		cellColor="#2c2c2a"
		sectionColor="#44443f"
		fadeDistance={12}
	/>
{/if}

{#each doc.fixtures ?? [] as f, i (i)}
	<Fixture3D fixture={f} />
{/each}

{#each doc.pipes ?? [] as p, i (p.id ?? i)}
	{@const ref = p.bind?.flowing}
	{@const neg = ref?.startsWith('!') ?? false}
	{@const v = ref ? readPath(tags, neg ? ref.slice(1) : ref) : undefined}
	<Pipe3D
		points={p.points}
		radius={p.radius}
		flowing={ref ? (neg ? v !== true : flowing(v)) : false}
		good={ref ? bindingsGood({ flowing: ref }, isGood) : true}
	/>
{/each}

{#each doc.nodes as n (n.id)}
	{@const def = defFor(n.id, n.kind)}
	{#if def}
		{@const Model = def.component}
		{@const value = n.tag ? tags[n.tag] : undefined}
		{@const bound = resolveNodeBindings(n.bind, tags)}
		{@const good = (n.tag ? isGood(n.tag) : true) && bindingsGood(n.bind, isGood)}
		{@const label = n.label ?? n.id}
		{@const alarm = n.tag ? alarms.get(n.tag) : undefined}
		{@const isSel = selected === n.id}
		{@const bounds = boundsOf(n.id, def.bounds)}
		<T.Group
			position={n.pos}
			rotation={rad(n.rot)}
			scale={n.scale ?? 1}
			onclick={(e: IntersectionEvent<MouseEvent>) => {
				if (e.delta > DRAG_PX) return;
				e.stopPropagation();
				onpick?.(n.id);
			}}
		>
			{#if def.data}
				<Model
					{value}
					{good}
					{label}
					selected={isSel}
					data={def.data}
					onbounds={(b: { size: Vec3; center: Vec3 }) => (autoBounds[n.id] = b)}
					onfail={() => (failed[n.id] = true)}
					{...n.props}
					{...bound}
				/>
			{:else}
				<Model {value} {good} {label} selected={isSel} {...n.props} {...bound} />
			{/if}
			{#if alarm}
				<Halo size={bounds.size} center={bounds.center} priority={alarm.priority} unacked={alarm.unacked} />
			{:else if isSel}
				<T.Mesh position={bounds.center}>
					<T.BoxGeometry args={bounds.size} />
					<T.MeshBasicMaterial color={palette.selected} wireframe transparent opacity={0.35} />
				</T.Mesh>
			{/if}
			<Label at={labelOf(def.labelAt, bounds)} title={label} value={def.status?.(value, good) ?? ''} {good} />
		</T.Group>
	{/if}
{/each}
