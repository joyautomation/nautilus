<script lang="ts">
	// Bench-only weather for the 3D view: rain streaks over the station, as
	// heavy as the simulated inflow, and a light that spikes when the page
	// fires a lightning flash. Rendered inside SceneView (its children run in
	// the Threlte scene). The field build has no InflowGpm, so the page never
	// mounts this there.
	import { T, useTask } from '@threlte/core';
	import { BufferAttribute, BufferGeometry, LineBasicMaterial } from 'three';

	let { intensity = 0, flash = 0 }: { intensity?: number; flash?: number } = $props();

	// The volume the rain falls through, in scene metres: wider than the
	// station so the edges of the frame are wet too. Drops stop at grade (y 0).
	const MAX = 3000;
	const X = [-6, 10];
	const Z = [-7, 7];
	const TOP = 7;
	const LEN = 0.35; // streak length
	const SPEED = 9; // m/s

	const pos = new Float32Array(MAX * 6);
	const rand = (a: number, b: number) => a + Math.random() * (b - a);
	function drop(i: number, y = rand(0, TOP)) {
		const x = rand(X[0], X[1]);
		const z = rand(Z[0], Z[1]);
		// A slight slant, as if blown.
		pos.set([x, y, z, x + 0.06, y + LEN, z], i * 6);
	}
	for (let i = 0; i < MAX; i++) drop(i);

	const geometry = new BufferGeometry();
	geometry.setAttribute('position', new BufferAttribute(pos, 3));
	const material = new LineBasicMaterial({ color: '#b9c8d6', transparent: true, opacity: 0.5, depthWrite: false });

	useTask((delta) => {
		const n = Math.round(MAX * intensity);
		geometry.setDrawRange(0, n * 2);
		if (n === 0) return;
		const dy = SPEED * Math.min(delta, 0.1);
		for (let i = 0; i < n; i++) {
			const o = i * 6;
			pos[o + 1] -= dy;
			pos[o + 4] -= dy;
			if (pos[o + 1] < 0) drop(i, TOP);
		}
		geometry.attributes.position.needsUpdate = true;
	});
</script>

<T.LineSegments {geometry} {material} frustumCulled={false} visible={intensity > 0} />
<!-- Lightning: a bright cold light high over the station, on only during a flash. -->
<T.DirectionalLight position={[-4, 12, 6]} color="#dfe8ff" intensity={flash * 9} />
<T.AmbientLight color="#dfe8ff" intensity={flash * 2.5} />
