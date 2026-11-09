<script lang="ts">
	// A pipe run: cylinders along a polyline with spheres at the bends.
	// Flow shows as the fluid colour, and as motion along the run in the
	// order of its points: in the lit and flat looks a train of bulges in
	// the pipe's own colour (a slug travelling the line, the way argonaut's
	// site pipes show it); on paper a train of small arrowheads riding just
	// above the pipe in the palette's quiet flow colour, fading in and out
	// at the run's ends, since a bulge on a white pipe against white paper
	// is nothing and black marks on the line itself read harsh. A
	// bad-quality binding greys the pipe out and stops the train.
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

	// The train: one marker every `pitch` of distance along the whole run
	// (segment to segment, so a slug crosses a bend instead of restarting
	// after it), all advancing together at `speed`. Chevrons are closer
	// together and quicker than bulges, as marks are to a moving mass.
	let total = $derived(segments.reduce((a, s) => a + s.len, 0));
	let pitch = $derived(view.paper ? Math.max(0.2, radius * 20) : Math.max(0.3, radius * 18));
	let speed = $derived(view.paper ? pitch * 1.2 : pitch * 1.2);
	// A paper arrowhead, and how far above the pipe's axis it rides.
	let head = $derived(radius * 1.1);
	let lift = $derived(radius * 2.3);
	// Over this distance from either end of the run an arrow fades.
	let fade = $derived(pitch * 0.8);
	// A bulge: a sphere a little wider than the pipe, stretched along it.
	let bulge = $derived(radius * 1.28);
	let margin = $derived(view.paper ? head : bulge * 2.2);
	let phase = $state(0);
	let moving = $derived(arrows && flowing && good);
	useTask((dt) => {
		if (moving) phase = (phase + speed * dt) % pitch;
	});
	let train = $derived.by(() => {
		if (!moving) return [];
		const out: { pos: Vec3; quat: [number, number, number, number]; alpha: number }[] = [];
		let k = 0;
		let before = 0;
		for (let d = phase; d < total; d += pitch) {
			while (k < segments.length - 1 && d > before + segments[k].len) before += segments[k++].len;
			const s = segments[k];
			const t = d - before;
			// Keep a marker off the ends of the run, where it would poke out
			// of the open pipe or into whatever the run meets.
			if (d < margin || d > total - margin) continue;
			const pos: Vec3 = [s.a[0] + s.dir[0] * t, s.a[1] + s.dir[1] * t, s.a[2] + s.dir[2] * t];
			if (!view.paper) {
				out.push({ pos, quat: s.quat, alpha: 1 });
				continue;
			}
			// Above the pipe: world up with the run's own direction taken
			// out (on a vertical run, beside it instead).
			const d3 = new Vector3(...s.dir);
			const off = up.clone().sub(d3.clone().multiplyScalar(up.dot(d3)));
			if (off.lengthSq() < 1e-4) off.set(1, 0, 0).sub(d3.clone().multiplyScalar(d3.x));
			off.normalize().multiplyScalar(lift);
			const alpha = Math.max(0, Math.min(1, (d - margin) / fade, (total - margin - d) / fade));
			out.push({ pos: [pos[0] + off.x, pos[1] + off.y, pos[2] + off.z], quat: s.quat, alpha });
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
{#each train as c}
	{#if view.paper}
		<!-- an arrowhead pointing +y, turned onto the segment, riding above
		     the pipe; no clicks, no ink of its own -->
		<T.Mesh position={c.pos} quaternion={c.quat} raycast={() => {}} userData={{ ink: false }}>
			<T.ConeGeometry args={[head, head * 2.2, 16]} />
			<T.MeshBasicMaterial color={palette.flow ?? palette.ink} transparent opacity={0.9 * c.alpha} depthWrite={false} />
		</T.Mesh>
	{:else}
		<!-- a slug: the pipe's own colour, so it reads as the pipe swelling -->
		<T.Mesh position={c.pos} quaternion={c.quat} scale={[1, 2.2, 1]} raycast={() => {}} userData={{ ink: false }}>
			<T.SphereGeometry args={[bulge, 20, 12]} />
			<T.MeshStandardMaterial {color} {...pbr} />
		</T.Mesh>
	{/if}
{/each}
