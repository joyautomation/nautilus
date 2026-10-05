<script lang="ts">
	// A pipe by ref (docs/design/spatial-hmi.md §3d): `flowing` names what a
	// document pipe's `bind.flowing` names, resolved by the same rule as a
	// node's bind — a path from the frame's tags at the top level, from the
	// enclosing node's struct inside one. Pipe3D stays the geometry.
	import { getContext } from 'svelte';
	import { SCENE, NODE, type SceneContext, type NodeContext } from '../context.js';
	import { readPath, readPart, bindingsGood, flowing as isFlowing } from '../bindings.js';
	import type { Vec3 } from '../scene.js';
	import Pipe3D from './Pipe3D.svelte';

	let { points, radius, flowing }: { points: Vec3[]; radius?: number; flowing?: string } = $props();
	const scene = getContext<SceneContext | undefined>(SCENE);
	const parent = getContext<NodeContext | undefined>(NODE);

	let neg = $derived(flowing?.startsWith('!') ?? false);
	let path = $derived(flowing ? (neg ? flowing.slice(1) : flowing) : undefined);
	let v = $derived(path === undefined ? undefined : parent ? readPart(parent.value, parent.over, path) : readPath(scene?.tags, path));
	let on = $derived(path === undefined ? false : neg ? v !== true : isFlowing(v));
	let good = $derived(parent ? parent.good : flowing ? bindingsGood({ flowing }, (t) => scene?.isGood(t) ?? true) : true);
</script>

<Pipe3D {points} {radius} flowing={on} {good} />
