<script lang="ts">
	// A patch cable along a path (rack.ts cablePath): a tube down the
	// straight runs, each bend rounded to a small radius the way a cable
	// lies (a spline through the points would loop where they are close).
	// Coloured by its link check; a cable the check cannot vouch for is
	// drawn thinner and faint. It takes no pick — the ports at its ends do.
	import { T } from '@threlte/core';
	import { CurvePath, LineCurve3, QuadraticBezierCurve3, TubeGeometry, Vector3 } from 'three';
	import type { Vec3 } from '../scene.js';

	let {
		points,
		color,
		radius = 0.0022,
		bend = 0.025,
		arc = false,
		faint = false
	}: {
		points: Vec3[];
		color: string;
		radius?: number;
		bend?: number;
		/** Three points: one smooth arc from the first to the last, the middle its control point (a mesh edge). */
		arc?: boolean;
		faint?: boolean;
	} = $props();

	function route(pts: Vector3[]): CurvePath<Vector3> {
		const path = new CurvePath<Vector3>();
		let from = pts[0];
		for (let i = 1; i < pts.length - 1; i++) {
			const p = pts[i];
			const r = Math.min(bend, p.distanceTo(pts[i - 1]) / 2, p.distanceTo(pts[i + 1]) / 2);
			const inAt = p.clone().add(pts[i - 1].clone().sub(p).setLength(r));
			const outAt = p.clone().add(pts[i + 1].clone().sub(p).setLength(r));
			if (from.distanceTo(inAt) > 1e-6) path.add(new LineCurve3(from, inAt));
			path.add(new QuadraticBezierCurve3(inAt, p, outAt));
			from = outAt;
		}
		path.add(new LineCurve3(from, pts[pts.length - 1]));
		return path;
	}
	const curveOf = (pts: Vector3[]) => (arc && pts.length === 3 ? new QuadraticBezierCurve3(pts[0], pts[1], pts[2]) : route(pts));
	let geometry = $derived(new TubeGeometry(curveOf(points.map((p) => new Vector3(...p))) as never, points.length * 32, faint ? radius * 0.7 : radius, 6, false));
	$effect(() => {
		const g = geometry;
		return () => g.dispose();
	});
</script>

<T.Mesh {geometry} raycast={() => {}}>
	<T.MeshStandardMaterial {color} roughness={0.7} transparent={faint} opacity={faint ? 0.45 : 1} />
</T.Mesh>
