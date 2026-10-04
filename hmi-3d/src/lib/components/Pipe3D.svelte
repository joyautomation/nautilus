<script lang="ts">
	// A pipe run: cylinders along a polyline with spheres at the bends.
	// Flow shows as the fluid colour; a bad-quality binding greys it out.
	import { T } from '@threlte/core';
	import { Quaternion, Vector3 } from 'three';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';
	import type { ViewState } from './view.js';

	let {
		points,
		radius = 0.012,
		flowing = false,
		good = true
	}: { points: Vec3[]; radius?: number; flowing?: boolean; good?: boolean } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };
	let pbr = $derived(view.lit ? { metalness: 0.7, roughness: 0.4 } : { metalness: 0, roughness: 0.6 });

	const up = new Vector3(0, 1, 0);
	let segments = $derived(
		points.slice(1).map((p, i) => {
			const a = new Vector3(...points[i]);
			const b = new Vector3(...p);
			const d = b.clone().sub(a);
			const q = new Quaternion().setFromUnitVectors(up, d.clone().normalize());
			const mid = a.add(b).multiplyScalar(0.5);
			return { pos: mid.toArray() as Vec3, quat: q.toArray() as [number, number, number, number], len: d.length() };
		})
	);
	let color = $derived(!good ? palette.stale : flowing ? palette.fluid : palette.steel);
</script>

{#each segments as s}
	<T.Mesh position={s.pos} quaternion={s.quat} castShadow>
		<T.CylinderGeometry args={[radius, radius, s.len, 12]} />
		<T.MeshStandardMaterial {color} {...pbr} />
	</T.Mesh>
{/each}
{#each points.slice(1, -1) as p}
	<T.Mesh position={p} castShadow>
		<T.SphereGeometry args={[radius, 12, 8]} />
		<T.MeshStandardMaterial {color} {...pbr} />
	</T.Mesh>
{/each}
