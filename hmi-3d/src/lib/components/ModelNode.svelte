<script lang="ts">
	// A model with drives, for a component of your own (design §3g, "Eject
	// to component"): what the kind editor writes when a kind file is
	// ejected to a .svelte — the same glTF, the same drive rows, now as
	// code beside which anything Threlte can be added. The glTF loader
	// stays a dynamic chunk (GltfNode is never statically exported), so a
	// scene with no model still pays nothing for it.
	import type { Component } from 'svelte';
	import type { NodeProps } from '../registry.js';
	import type { SceneKind, Vec3 } from '../scene.js';

	type Props = NodeProps & {
		/** The model and its drives, as a kind file holds them. */
		data: SceneKind;
		onbounds?: (b: { size: Vec3; center: Vec3 }) => void;
		onparts?: (parts: Record<string, { size: Vec3; center: Vec3 }>) => void;
		onfail?: (err: unknown) => void;
	};
	let { ...props }: Props = $props();
	let Gltf = $state<Component<Props> | null>(null);
	$effect(() => {
		let live = true;
		import('./GltfNode.svelte').then((m) => {
			if (live) Gltf = m.default as unknown as Component<Props>;
		});
		return () => {
			live = false;
		};
	});
</script>

{#if Gltf}
	<Gltf {...props} />
{/if}
