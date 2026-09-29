<script lang="ts" module>
	// One part from the hardware glTF library (models/hardware/*.glb),
	// scaled to the size the chassis profile gives it: the library is the
	// look, the profile is the truth. Loaded files are cached per URL for
	// the page's life; each placement gets a clone with its own materials,
	// so lighting one drive's LED never lights the next.
	import { GLTFLoader, type GLTF } from 'three/examples/jsm/loaders/GLTFLoader.js';

	const cache = new Map<string, Promise<GLTF>>();
	let loader: GLTFLoader | undefined;
	export function loadPart(url: string): Promise<GLTF> {
		let p = cache.get(url);
		if (!p) {
			loader ??= new GLTFLoader();
			p = loader.loadAsync(url);
			p.catch(() => cache.delete(url));
			cache.set(url, p);
		}
		return p;
	}
</script>

<script lang="ts">
	import { T, useTask } from '@threlte/core';
	import { Box3, Color, Mesh, MeshStandardMaterial, Object3D, Vector3 } from 'three';
	import { onDestroy } from 'svelte';
	import type { Vec3 } from '../scene.js';

	let {
		src,
		size,
		led,
		accent,
		opacity = 1,
		spin = 0,
		ports,
		tint,
		fallback = '#55585c'
	}: {
		src: string;
		/** Metres; the model is stretched to exactly this box, centred. */
		size: Vec3;
		/** `Led` mesh colour, emissive; undefined = dark. */
		led?: string;
		/** `Accent` mesh colour; undefined = as modelled. */
		accent?: string;
		/** Below 1 the whole part turns translucent (stale, x-ray, unverified). */
		opacity?: number;
		/** Revolutions per second of the `Rotor` mesh about z. */
		spin?: number;
		/** Show `Port1`…`PortN`, hide the rest. */
		ports?: number;
		/** An overlay's colour: blended into every material and lit a little,
		 * so the part reads as that colour whatever it is made of. */
		tint?: string;
		/** The box colour until the model arrives (or if it cannot). */
		fallback?: string;
	} = $props();

	let root = $state.raw<Object3D | null>(null);
	const mats: MeshStandardMaterial[] = [];
	/** Each material's modelled colour, so a tint can be taken off again. */
	const base = new Map<MeshStandardMaterial, Color>();
	let ledMats: MeshStandardMaterial[] = [];
	let accentMats: MeshStandardMaterial[] = [];
	let rotor: Object3D | undefined;
	let portMeshes: Object3D[] = [];
	let failed = $state(false);

	$effect(() => {
		const url = src;
		let live = true;
		loadPart(url).then(
			(g) => {
				if (!live) return;
				const o = g.scene.clone(true);
				o.traverse((n) => {
					if (!(n instanceof Mesh)) return;
					const src = Array.isArray(n.material) ? n.material[0] : n.material;
					if (!(src instanceof MeshStandardMaterial)) return;
					const m = src.clone();
					n.material = m;
					mats.push(m);
					base.set(m, m.color.clone());
					if (n.name.startsWith('Led')) ledMats.push(m);
					if (n.name.startsWith('Accent')) accentMats.push(m);
					if (/^Port\d/.test(n.name)) portMeshes.push(n);
				});
				rotor = o.getObjectByName('Rotor') ?? undefined;
				const b = new Box3().setFromObject(o);
				const s = b.getSize(new Vector3());
				const c = b.getCenter(new Vector3());
				nominal = [s.x || 1, s.y || 1, s.z || 1];
				centre = [c.x, c.y, c.z];
				root = o;
			},
			() => live && (failed = true)
		);
		return () => {
			live = false;
		};
	});

	// Stretch to the profile's box, centred on the part's origin.
	let nominal = $state<Vec3>([1, 1, 1]);
	let centre = $state<Vec3>([0, 0, 0]);
	let scale = $derived<Vec3>([size[0] / nominal[0], size[1] / nominal[1], size[2] / nominal[2]]);
	let offset = $derived<Vec3>([-centre[0] * scale[0], -centre[1] * scale[1], -centre[2] * scale[2]]);

	$effect(() => {
		if (!root) return;
		const t = tint ? new Color(tint) : undefined;
		for (const m of mats) {
			m.color.copy(base.get(m) ?? m.color);
			if (t) m.color.lerp(t, 0.7);
			m.emissive = t ? t.clone().multiplyScalar(0.35) : new Color(0);
			m.emissiveIntensity = t ? 1 : 0;
		}
		for (const m of ledMats) {
			if (t) continue;
			m.color.set(led ?? '#111');
			m.emissive = new Color(led ?? '#000');
			m.emissiveIntensity = led ? 1.6 : 0;
		}
		if (!t) for (const m of accentMats) if (accent) m.color.set(accent);
		for (const m of mats) {
			m.transparent = opacity < 1;
			m.opacity = opacity;
			m.depthWrite = opacity >= 1;
			m.needsUpdate = true;
		}
		portMeshes.forEach((p) => {
			const n = Number(p.name.slice(4));
			p.visible = ports === undefined || n <= ports;
		});
	});

	useTask((dt) => {
		if (rotor && spin) rotor.rotation.z = (rotor.rotation.z + spin * 2 * Math.PI * dt) % (2 * Math.PI);
	});

	onDestroy(() => mats.forEach((m) => m.dispose()));
</script>

{#if root}
	<T.Group position={offset} scale={scale}>
		<T is={root} />
	</T.Group>
{:else}
	<T.Mesh>
		<T.BoxGeometry args={size} />
		<T.MeshStandardMaterial color={tint ?? fallback} transparent={opacity < 1 || failed} opacity={failed ? 0.5 : opacity} />
	</T.Mesh>
{/if}
