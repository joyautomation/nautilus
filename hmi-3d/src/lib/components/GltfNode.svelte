<script lang="ts" module>
	// A data kind (docs/design/spatial-hmi.md §3c): a glTF whose named meshes
	// the kind's drives move, tint, scale, light or hide from the node's
	// struct value. Nothing in here knows a pump from a tank — the model
	// and the drives come from the document.
	//
	// This component is a DYNAMIC import from Scene3D, so the glTF loader
	// only reaches the browser for a scene that has a model. Loaded files
	// are cached per URL for the page's life; each node gets a clone of the
	// scene graph (geometry shared, materials cloned only where a drive or
	// stale quality touches them, and those clones disposed on unmount).
	import { GLTFLoader, type GLTF } from 'three/examples/jsm/loaders/GLTFLoader.js';

	const cache = new Map<string, Promise<GLTF>>();
	let loader: GLTFLoader | undefined;
	export function loadModel(url: string): Promise<GLTF> {
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
	import { Box3, Color, Euler, Mesh, MeshStandardMaterial, Object3D, Vector3 } from 'three';
	import { getContext, onDestroy } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { evalDrives, type MeshState } from '../drives.js';
	import type { SceneKind, Vec3 } from '../scene.js';
	import type { NodeProps } from '../registry.js';

	let {
		value,
		good = true,
		data,
		onbounds,
		onfail,
		label: _label,
		selected: _selected,
		...over
	}: NodeProps & {
		/** The document's kind entry: model + drives. */
		data: SceneKind;
		/** The model's local box, once loaded — the halo and label use it. */
		onbounds?: (b: { size: Vec3; center: Vec3 }) => void;
		/** The model could not be loaded; Scene3D falls back to the Svelte kind. */
		onfail?: (err: unknown) => void;
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	let root = $state<Object3D | null>(null);
	const meshes = new Map<string, Object3D>();
	const baseRot = new Map<Object3D, Euler>();
	/** Materials this node cloned (so a tint on one pump never paints the next). */
	const owned = new Map<Mesh, MeshStandardMaterial>();
	const original = new Map<Mesh, Mesh['material']>();
	let staleMat: MeshStandardMaterial | undefined;
	let missing = new Set<string>();

	function ownMaterial(m: Mesh): MeshStandardMaterial | undefined {
		let mat = owned.get(m);
		if (mat) return mat;
		const src = Array.isArray(m.material) ? m.material[0] : m.material;
		if (!(src instanceof MeshStandardMaterial)) return undefined;
		mat = src.clone();
		owned.set(m, mat);
		return mat;
	}

	/** A palette slot or a CSS colour. */
	function colour(name: string): string {
		const p = palette as unknown as Record<string, unknown>;
		if (typeof p[name] === 'string') return p[name] as string;
		if (palette.priority[name]) return palette.priority[name];
		return name;
	}

	$effect(() => {
		const url = data.model;
		if (!url) return;
		let cancelled = false;
		loadModel(url).then(
			(gltf) => {
				if (cancelled) return;
				const clone = gltf.scene.clone(true);
				clone.traverse((o) => {
					if (o.name) meshes.set(o.name, o);
					if (o instanceof Mesh) {
						o.castShadow = true;
						o.receiveShadow = true;
						original.set(o, o.material);
					}
					baseRot.set(o, o.rotation.clone());
				});
				clone.updateMatrixWorld(true);
				const box = new Box3().setFromObject(clone);
				const size = new Vector3();
				const center = new Vector3();
				box.getSize(size);
				box.getCenter(center);
				onbounds?.({ size: size.toArray() as Vec3, center: center.toArray() as Vec3 });
				missing = new Set((data.drive ?? []).map((d) => d.mesh).filter((m) => !meshes.has(m)));
				if (missing.size)
					console.warn(`hmi-3d: ${url} has no mesh named ${[...missing].join(', ')} (it has ${[...meshes.keys()].join(', ')})`);
				root = clone;
			},
			(err) => {
				if (cancelled) return;
				console.warn(`hmi-3d: could not load ${url}:`, err);
				onfail?.(err);
			}
		);
		return () => {
			cancelled = true;
		};
	});

	let states = $derived(evalDrives(data.drive, value, over as Record<string, unknown>));
	let spins = $derived(states.filter((s) => s.spin && s.spin.radPerS !== 0));

	// Apply everything but spin whenever the value (or quality) changes.
	$effect(() => {
		const r = root;
		if (!r) return;
		void good;
		for (const s of states) apply(s);
		applyQuality();
	});

	function apply(s: MeshState) {
		const o = meshes.get(s.mesh);
		if (!o) return;
		if (s.turn) {
			const base = baseRot.get(o)!;
			o.rotation.copy(base);
			o.rotation[s.turn.axis] = base[s.turn.axis] + s.turn.rad;
		}
		if (s.scale) o.scale.set(...s.scale);
		if (s.visible !== undefined) o.visible = s.visible;
		if (o instanceof Mesh && (s.tint !== undefined || s.emissive !== undefined)) {
			const mat = ownMaterial(o);
			if (!mat) return;
			const src = original.get(o);
			const srcMat = (Array.isArray(src) ? src[0] : src) as MeshStandardMaterial | undefined;
			if (s.tint !== undefined) mat.color.set(s.tint ? colour(s.tint) : (srcMat?.color ?? new Color('#888')));
			if (s.emissive !== undefined) {
				if (s.emissive) {
					mat.emissive.set(colour(s.emissive.color));
					mat.emissiveIntensity = s.emissive.intensity;
				} else {
					mat.emissive.copy(srcMat?.emissive ?? new Color(0));
					mat.emissiveIntensity = srcMat?.emissiveIntensity ?? 1;
				}
			}
			if (good) o.material = mat;
		}
	}

	/** Bad quality: every mesh in the stale grey, translucent — a stale
	 * model must never look like a healthy one. Good again: its own paint. */
	function applyQuality() {
		for (const [m, orig] of original) {
			if (!good) {
				staleMat ??= new MeshStandardMaterial({ color: palette.stale, transparent: true, opacity: 0.45, metalness: 0, roughness: 0.8 });
				m.material = staleMat;
			} else m.material = owned.get(m) ?? orig;
		}
	}

	useTask((dt) => {
		if (!root || !good) return;
		for (const s of spins) {
			const o = meshes.get(s.mesh);
			if (o) o.rotation[s.spin!.axis] += s.spin!.radPerS * dt;
		}
	});

	onDestroy(() => {
		for (const m of owned.values()) m.dispose();
		staleMat?.dispose();
	});
</script>

{#if root}
	<T is={root} />
{/if}
