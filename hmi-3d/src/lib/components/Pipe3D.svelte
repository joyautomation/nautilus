<script lang="ts">
	// A pipe run: cylinders along a polyline with spheres at the bends.
	// Flow shows on the pipe itself, as a 3D dashed line: while it flows the
	// run is the fluid colour, banded at a regular pitch in the palette's
	// flow colour, and the bands travel along it in the order of its points
	// (across bends, not restarting after each), the way the 2D mimic's
	// dashes move along a pipe. The same in every look; the palette sets the
	// two colours (a lighter blue on the lit fluid, a quiet blue on paper's
	// near-white, a light one on dark paper). A bad-quality binding greys
	// the pipe out and stops the bands.
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
	let bandColor = $derived(palette.flow ?? '#9cc8f2');

	// The bands: one every `pitch` of distance along the whole run, half of
	// it banded, all advancing together at `speed`.
	let total = $derived(segments.reduce((a, s) => a + s.len, 0));
	let pitch = $derived(Math.max(0.12, radius * 6));
	let band = $derived(pitch * 0.5);
	let speed = $derived(pitch * 1.5);
	// A band sits just proud of the pipe, so it covers the pipe's own colour.
	let bandRadius = $derived(radius * 1.03);
	let phase = $state(0);
	let moving = $derived(arrows && flowing && good);
	useTask((dt) => {
		if (moving) phase = (phase + speed * dt) % pitch;
	});
	let bands = $derived.by(() => {
		if (!moving) return [];
		const out: { pos: Vec3; quat: [number, number, number, number]; len: number }[] = [];
		// Each band is [start, start + band) along the run, cut where it
		// crosses a segment end so it follows the bend.
		for (let start = phase - pitch; start < total; start += pitch) {
			const end = start + band;
			let before = 0;
			for (const s of segments) {
				const lo = Math.max(start, before);
				const hi = Math.min(end, before + s.len);
				if (hi - lo > 1e-4) {
					const t = (lo + hi) / 2 - before;
					out.push({ pos: [s.a[0] + s.dir[0] * t, s.a[1] + s.dir[1] * t, s.a[2] + s.dir[2] * t], quat: s.quat, len: hi - lo });
				}
				before += s.len;
			}
		}
		return out;
	});
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
{#each bands as b}
	<!-- a band of the flow colour around the pipe; no clicks, no ink of its own -->
	<!-- unit length, scaled to the band: no new geometry every frame -->
	<T.Mesh position={b.pos} quaternion={b.quat} scale={[1, b.len, 1]} raycast={() => {}} userData={{ ink: false }}>
		<T.CylinderGeometry args={[bandRadius, bandRadius, 1, facets, 1, true]} />
		<T.MeshStandardMaterial color={bandColor} {...pbr} />
	</T.Mesh>
{/each}
