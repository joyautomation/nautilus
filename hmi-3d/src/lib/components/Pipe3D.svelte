<script lang="ts">
	// A pipe run: cylinders along a polyline with spheres at the bends.
	// Flow shows as the fluid colour, and as chevrons travelling the run in
	// the order of its points; a bad-quality binding greys it out and stops
	// them. In the paper look the pipe stays white and the chevrons are the
	// whole story.
	import { T, useTask } from '@threlte/core';
	import { Quaternion, Vector3 } from 'three';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { Vec3 } from '../scene.js';
	import type { ViewState } from './view.js';

	let {
		points,
		radius = 0.012,
		flowing = false,
		good = true,
		arrows = true
	}: { points: Vec3[]; radius?: number; flowing?: boolean; good?: boolean; arrows?: boolean } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: false };
	let pbr = $derived(view.lit ? { metalness: 0.7, roughness: 0.4 } : { metalness: 0, roughness: 0.6 });
	// Enough facets that the paper look's edge threshold draws a pipe as
	// its silhouette and not as a bundle of longitudinal lines.
	let facets = $derived(view.paper ? 32 : 12);

	const up = new Vector3(0, 1, 0);
	let segments = $derived(
		points.slice(1).map((p, i) => {
			const a = new Vector3(...points[i]);
			const b = new Vector3(...p);
			const d = b.clone().sub(a);
			const q = new Quaternion().setFromUnitVectors(up, d.clone().normalize());
			const mid = a.clone().add(b).multiplyScalar(0.5);
			return { a: a.toArray() as Vec3, dir: d.clone().normalize().toArray() as Vec3, pos: mid.toArray() as Vec3, quat: q.toArray() as [number, number, number, number], len: d.length() };
		})
	);
	let color = $derived(!good ? palette.stale : flowing ? palette.fluid : palette.steel);

	// The chevrons: one every `pitch` along each segment, all advancing
	// together at `speed`, so a run reads as one stream. Sized off the
	// pipe, standing proud of it, so they show on an opaque pipe too.
	let pitch = $derived(Math.max(0.12, radius * 12));
	let speed = $derived(pitch * 2);
	let head = $derived(radius * 1.8);
	let phase = $state(0);
	let moving = $derived(arrows && flowing && good);
	useTask((dt) => {
		if (moving) phase = (phase + speed * dt) % pitch;
	});
	let chevrons = $derived.by(() => {
		if (!moving) return [];
		const out: { pos: Vec3; quat: [number, number, number, number] }[] = [];
		for (const s of segments) {
			const n = Math.floor((s.len - head * 2) / pitch);
			if (n < 1) continue;
			// Centre the train in the segment, then slide it by the phase.
			const start = (s.len - (n - 1) * pitch) / 2 - pitch / 2;
			for (let k = 0; k < n; k++) {
				const t = start + k * pitch + phase;
				if (t < head || t > s.len - head) continue;
				out.push({ pos: [s.a[0] + s.dir[0] * t, s.a[1] + s.dir[1] * t, s.a[2] + s.dir[2] * t], quat: s.quat });
			}
		}
		return out;
	});
	let chevronColor = $derived(view.paper ? palette.ink : palette.shell);
</script>

{#each segments as s}
	<T.Mesh position={s.pos} quaternion={s.quat} castShadow>
		<T.CylinderGeometry args={[radius, radius, s.len, facets]} />
		<T.MeshStandardMaterial {color} {...pbr} />
	</T.Mesh>
{/each}
{#each points.slice(1, -1) as p}
	<T.Mesh position={p} castShadow>
		<T.SphereGeometry args={[radius, facets, facets / 2]} />
		<T.MeshStandardMaterial {color} {...pbr} />
	</T.Mesh>
{/each}
{#each chevrons as c}
	<!-- an arrowhead pointing +y, turned onto the segment; no clicks, no ink -->
	<T.Mesh position={c.pos} quaternion={c.quat} raycast={() => {}} userData={{ ink: false }}>
		<T.ConeGeometry args={[head, head * 2, 4]} />
		<T.MeshBasicMaterial color={chevronColor} />
	</T.Mesh>
{/each}
