<script lang="ts">
	// The paper look's lines (docs/design/spatial-hmi.md §3f): every mesh
	// in the scene gets its edges drawn, as line segments that are children
	// of the mesh and so follow its every move — a turning coupling, a
	// fluid column that grows. Nothing in a kind has to know: a kind that
	// draws a glow, a chevron or a helper box opts a mesh out with
	// `userData={{ ink: false }}`, and wireframes and shader materials (the
	// halo, the selection box, the grid) are skipped on their own.
	//
	// The graph is walked once a frame. New meshes get their lines, a mesh
	// whose geometry was regenerated gets them cut again, and everything
	// else is one WeakMap lookup. Fills are pushed back a hair with a
	// polygon offset so a line that lies on a face wins the depth test
	// instead of flickering through it; hidden lines stay hidden, as in a
	// drawing. Everything is undone when the look changes.
	import { useThrelte, useTask } from '@threlte/core';
	import { onDestroy } from 'svelte';
	import { EdgesGeometry, LineBasicMaterial, LineSegments, Mesh, type BufferGeometry, type Material, type Object3D } from 'three';

	let {
		color,
		/** Degrees between two faces' normals before the edge between them is drawn. */
		threshold = 20
	}: { color: string; threshold?: number } = $props();
	const { scene, invalidate } = useThrelte();

	const material = new LineBasicMaterial();
	$effect(() => {
		material.color.set(color);
		invalidate();
	});

	interface Inked {
		lines: LineSegments;
		from: BufferGeometry;
	}
	const inked = new WeakMap<Mesh, Inked>();
	const live = new Set<Mesh>();
	const pushed = new Map<Material, [boolean, number, number]>();

	const skip = (m: Mesh): boolean => {
		if (m.userData?.ink === false) return true;
		const mats = Array.isArray(m.material) ? m.material : [m.material];
		return mats.some((mat) => !mat || (mat as { wireframe?: boolean }).wireframe || mat.type === 'ShaderMaterial' || mat.type === 'RawShaderMaterial');
	};

	const push = (m: Mesh) => {
		for (const mat of Array.isArray(m.material) ? m.material : [m.material]) {
			if (!mat || pushed.has(mat)) continue;
			pushed.set(mat, [mat.polygonOffset, mat.polygonOffsetFactor, mat.polygonOffsetUnits]);
			mat.polygonOffset = true;
			mat.polygonOffsetFactor = 1;
			mat.polygonOffsetUnits = 1;
		}
	};

	const ink = (m: Mesh) => {
		const lines = new LineSegments(new EdgesGeometry(m.geometry, threshold), material);
		// A line is picked within a metre of the ray; these take no clicks.
		lines.raycast = () => {};
		lines.frustumCulled = false;
		lines.userData.ink = false;
		m.add(lines);
		inked.set(m, { lines, from: m.geometry });
		live.add(m);
		push(m);
	};

	const unink = (m: Mesh) => {
		const e = inked.get(m);
		if (!e) return;
		m.remove(e.lines);
		e.lines.geometry.dispose();
		inked.delete(m);
		live.delete(m);
	};

	useTask(() => {
		const seen = new Set<Mesh>();
		scene.traverse((o: Object3D) => {
			if (!(o as Mesh).isMesh || (o as { isInstancedMesh?: boolean }).isInstancedMesh || (o as { isSkinnedMesh?: boolean }).isSkinnedMesh) return;
			const m = o as Mesh;
			if (skip(m)) return;
			seen.add(m);
			const e = inked.get(m);
			if (!e) ink(m);
			else if (e.from !== m.geometry) {
				unink(m);
				ink(m);
			} else push(m);
		});
		// A mesh that left the scene takes its lines with it; forget it.
		for (const m of live) if (!seen.has(m)) unink(m);
	});

	onDestroy(() => {
		for (const m of [...live]) unink(m);
		for (const [mat, [on, f, u]] of pushed) {
			mat.polygonOffset = on;
			mat.polygonOffsetFactor = f;
			mat.polygonOffsetUnits = u;
		}
		pushed.clear();
		material.dispose();
		invalidate();
	});
</script>
