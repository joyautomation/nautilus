<script lang="ts">
	// The document's node as a component (docs/design/spatial-hmi.md §3d):
	// the same props a *.scene.json node has, prop for prop. It resolves
	// the node's value and quality from the scene it is inside, applies
	// `bind`, and wraps what it renders — the registry's kind, an
	// assembly's parts, or the children snippet — in the group, the alarm
	// halo, the selection box and the label. Scene3D renders every document
	// node through this component, so a hand-written <Node> and a placed one
	// are the same code path.
	//
	// A <Node> inside a <Node> is a PART: its `tag` is a member of the
	// enclosing node's struct, its refs resolve from that struct, it has no
	// label unless given, no halo, and a click on it picks the enclosing
	// node. That is how a component composes kinds, and how a data
	// assembly's parts render.
	import { T } from '@threlte/core';
	import { BoxGeometry } from 'three';
	import type { IntersectionEvent } from '@threlte/extras';
	import { getContext, setContext, type Snippet } from 'svelte';
	import { SCENE, NODE, type SceneContext, type NodeContext } from '../context.js';
	import { readPath, readPart, resolveRefs, bindingsGood } from '../bindings.js';
	import type { Vec3 } from '../scene.js';
	import type { NodeProps, Box } from '../registry.js';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import Halo from './Halo.svelte';
	import AlarmMarker from './AlarmMarker.svelte';
	import Label from './Label.svelte';
	import Pipe from './Pipe.svelte';
	import Self from './Node.svelte';

	let {
		id,
		kind,
		tag,
		label,
		pos,
		rot,
		scale = 1,
		props,
		bind,
		status,
		bounds: box,
		marker = true,
		children
	}: {
		id?: string;
		kind?: string;
		tag?: string;
		label?: string;
		pos: Vec3;
		rot?: Vec3;
		scale?: number;
		props?: Record<string, unknown>;
		bind?: Record<string, string>;
		/** The label's value text when there is no kind to supply it (a
		 * component used as a component: pass its `kind` export's status). */
		status?: (value: unknown, good: boolean) => string;
		/** The halo and selection box when no kind supplies one, or when this
		 * node's size is its own (a part sized by a chassis profile). */
		bounds?: Box;
		/** Float the alarm sign over the node while it is in alarm (the halo
		 * shows either way). Off where a wider marker speaks for it: a
		 * server's chassis, whose marker rolls up every part. */
		marker?: boolean;
		/** Render anything with the node's props instead of the kind's component. */
		children?: Snippet<[NodeProps]>;
	} = $props();

	const scene = getContext<SceneContext | undefined>(SCENE);
	const parent = getContext<NodeContext | undefined>(NODE);
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	if (!scene) console.warn('hmi-3d: <Node> renders nothing outside <Scene3D>');

	const deg = Math.PI / 180;
	const rad = (r?: Vec3): Vec3 => (r ? [r[0] * deg, r[1] * deg, r[2] * deg] : [0, 0, 0]);
	const DRAG_PX = 5;

	// A ref is a path from the current root: the frame's tags at the top
	// level, the enclosing node's struct inside one.
	const read = (path: string) => (parent ? readPart(parent.value, parent.over, path) : readPath(scene?.tags, path));
	let value = $derived(parent ? (tag ? readPart(parent.value, parent.over, tag) : parent.value) : tag ? scene?.tags[tag] : undefined);
	let over = $derived(resolveRefs(bind, read));
	let good = $derived(parent ? parent.good : (tag ? (scene?.isGood(tag) ?? true) : true) && bindingsGood(bind, (t) => scene?.isGood(t) ?? true));
	let def = $derived(kind && scene ? scene.defFor(id, kind) : undefined);
	// A part is unlabelled unless the author labels it.
	let title = $derived(label ?? (parent ? '' : (id ?? tag ?? '')));
	let alarm = $derived(!parent && tag ? scene?.alarms.get(tag) : undefined);
	let isSel = $derived(!parent && id !== undefined && scene?.selected === id);
	let isHover = $derived(!parent && id !== undefined && scene?.hovered === id);
	let bounds = $derived<Box>(box ?? (def && scene ? scene.boundsOf(id, def.bounds) : { size: [0.3, 0.3, 0.3], center: [0, 0.15, 0] }));
	let labelAt = $derived<Vec3>(def?.labelAt ?? [bounds.center[0], bounds.center[1] + bounds.size[1] / 2 + 0.02, bounds.center[2]]);
	let nodeProps = $derived<NodeProps>({ value, good, label: title, selected: isSel, ...props, ...over });
	let pickable = $derived(!parent && id !== undefined);

	setContext<NodeContext>(NODE, {
		get value() {
			return value;
		},
		get good() {
			return good;
		},
		get over() {
			return over;
		},
		get id() {
			return id;
		}
	});

	$effect(() => {
		if (parent || !scene || id === undefined) return;
		return scene.register({ id, tag, kind, label: title });
	});
	// A node that goes away while hovered must not leave the cursor behind.
	$effect(() => () => {
		if (scene && id !== undefined && scene.hovered === id) scene.hover(null);
	});
</script>

{#if scene}
	<T.Group
		position={pos}
		rotation={rad(rot)}
		{scale}
		onpointerenter={pickable
			? (e: IntersectionEvent<PointerEvent>) => {
					// Only the frontmost node under the pointer: whatever is
					// behind it gets its leave.
					e.stopPropagation();
					scene.hover(id!);
				}
			: undefined}
		onpointerleave={pickable
			? () => {
					if (scene.hovered === id) scene.hover(null);
				}
			: undefined}
		onclick={pickable
			? (e: IntersectionEvent<MouseEvent>) => {
					if (e.delta > DRAG_PX) return;
					e.stopPropagation();
					scene.pick(id!);
				}
			: undefined}
	>
		{#if children}
			{@render children(nodeProps)}
		{:else if def?.assembly}
			{#each def.assembly.nodes as part (part.id)}
				<Self {...part} />
			{/each}
			{#each def.assembly.pipes ?? [] as p, i (p.id ?? i)}
				<Pipe points={p.points} radius={p.radius} flowing={p.bind?.flowing} />
			{/each}
		{:else if def?.component}
			{@const Model = def.component}
			{#if def.data}
				<Model
					{...nodeProps}
					data={def.data}
					onbounds={(b: Box) => scene.reportBounds(id, b)}
					onfail={() => scene.reportFailed(id)}
				/>
			{:else}
				<Model {...nodeProps} />
			{/if}
		{/if}
		{#if isHover}
			<!-- Hover: the node a click would pick glows — edges plus a faint
			     fill in the accent colour. Neither takes a raycast (a line is
			     picked within 1 m of the ray). -->
			<T.LineSegments position={bounds.center} raycast={() => {}}>
				<T.EdgesGeometry args={[new BoxGeometry(...bounds.size.map((v) => v * 1.04))]} />
				<T.LineBasicMaterial color={palette.hover} />
			</T.LineSegments>
			<T.Mesh position={bounds.center} raycast={() => {}}>
				<T.BoxGeometry args={bounds.size.map((v) => v * 1.04) as Vec3} />
				<T.MeshBasicMaterial color={palette.hover} transparent opacity={0.18} depthWrite={false} />
			</T.Mesh>
		{/if}
		{#if alarm}
			<Halo size={bounds.size} center={bounds.center} priority={alarm.priority} unacked={alarm.unacked} />
			{#if marker}
				<AlarmMarker at={[bounds.center[0], bounds.center[1] + bounds.size[1] / 2 + 0.012, bounds.center[2]]} priority={alarm.priority} unacked={alarm.unacked} active={alarm.active} />
			{/if}
		{:else if isSel}
			<T.Mesh position={bounds.center}>
				<T.BoxGeometry args={bounds.size} />
				<T.MeshBasicMaterial color={palette.selected} wireframe transparent opacity={0.35} />
			</T.Mesh>
		{/if}
		{#if title}
			<Label at={labelAt} title={title} value={(status ?? def?.status)?.(value, good) ?? ''} {good} />
		{/if}
	</T.Group>
{/if}
