<script lang="ts">
	// Inside the <Canvas>: the renderer's Scene3D on the one-node document,
	// and the turntable — the node's own group turned a little each frame
	// (PlacedNode.object, the group the scene editor's gizmo moves), so
	// nothing is written anywhere and the document stays as composed.
	import { useTask } from '@threlte/core';
	import { Scene3D, type NodeRegistry, type PlacedNode, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import { kd } from './kindState.svelte';

	let { doc, registry, tags }: { doc: SceneDoc; registry: NodeRegistry; tags: Record<string, unknown> } = $props();

	let placed = $state<Record<string, PlacedNode>>({});
	const RAD_PER_S = 0.4;
	useTask((dt) => {
		const obj = placed.PREVIEW?.object;
		if (!obj || !kd.turntable) return;
		obj.rotation.y = (obj.rotation.y + dt * RAD_PER_S) % (Math.PI * 2);
	});
	// Harness hook: what is on stage, read-only.
	$effect(() => {
		const w = window as unknown as { __nxKind?: unknown };
		w.__nxKind = { rotation: () => placed.PREVIEW?.object?.rotation.y ?? null };
		return () => {
			delete w.__nxKind;
		};
	});
</script>

<Scene3D {doc} {registry} {tags} look={kd.look} bind:placed />
