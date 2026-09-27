<script lang="ts">
	// Renders a scene document inside a Threlte <Canvas>. Every node is
	// looked up in the registry, handed its struct tag and its resolved
	// bindings, and wrapped with its label, its alarm halo and a selection
	// outline — so a scene is fully described by its JSON file and this
	// component knows no kind by name.
	import { T } from '@threlte/core';
	import { Grid, OrbitControls, interactivity, type IntersectionEvent } from '@threlte/extras';
	import { resolveNodeBindings, bindingsGood, readPath, flowing } from '../bindings.js';
	import type { SceneDoc, Vec3 } from '../scene.js';
	import type { NodeRegistry } from '../registry.js';
	import type { AssetAlarm } from '../alarms.js';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
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
		/** A node was clicked (not dragged). Never fires for a click on nothing. */
		onpick?: (id: string) => void;
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	// Threlte's pointer plugin: every T.Group below with an onclick becomes
	// a raycast target. A click whose pointer travelled more than 5 px since
	// pointerdown is an orbit, not a pick.
	interactivity();
	const DRAG_PX = 5;

	const deg = Math.PI / 180;
	const rad = (r?: Vec3): Vec3 => (r ? [r[0] * deg, r[1] * deg, r[2] * deg] : [0, 0, 0]);

	let cam = $derived(doc.camera ?? { pos: [2, 1.5, 2.5] as Vec3, target: [0, 0, 0] as Vec3 });
	let grid = $derived(doc.grid);
</script>

<T.PerspectiveCamera makeDefault position={cam.pos} fov={cam.fov ?? 45} near={0.01} far={100}>
	<OrbitControls target={cam.target} enableDamping />
</T.PerspectiveCamera>
<T.AmbientLight intensity={0.6} />
<T.DirectionalLight position={[3, 5, 4]} intensity={1.6} />

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
	{@const def = registry[n.kind]}
	{#if def}
		{@const Model = def.component}
		{@const value = n.tag ? tags[n.tag] : undefined}
		{@const bound = resolveNodeBindings(n.bind, tags)}
		{@const good = (n.tag ? isGood(n.tag) : true) && bindingsGood(n.bind, isGood)}
		{@const label = n.label ?? n.id}
		{@const alarm = n.tag ? alarms.get(n.tag) : undefined}
		{@const isSel = selected === n.id}
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
			<Model {value} {good} {label} selected={isSel} {...n.props} {...bound} />
			{#if alarm}
				<Halo size={def.bounds.size} center={def.bounds.center} priority={alarm.priority} unacked={alarm.unacked} />
			{:else if isSel}
				<T.Mesh position={def.bounds.center}>
					<T.BoxGeometry args={def.bounds.size} />
					<T.MeshBasicMaterial color={palette.selected} wireframe transparent opacity={0.35} />
				</T.Mesh>
			{/if}
			<Label at={def.labelAt} title={label} value={def.status?.(value, good) ?? ''} {good} />
		</T.Group>
	{/if}
{/each}
