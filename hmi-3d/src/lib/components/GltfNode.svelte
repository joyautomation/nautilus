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
	//
	// The look (view.ts) picks the materials: lit wears the model's own;
	// flat is the same colours with no metalness and no maps, the values
	// the built-in kinds use; paper is a near-white fill (a translucent
	// material — a fluid — the palette's fluid grey) for PaperOutlines to
	// ink. A drive's tint or emissive goes on top of whichever is worn.
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
	import { Box3, Color, Euler, Mesh, MeshStandardMaterial, Object3D, Vector3, type Material } from 'three';
	import { getContext, onDestroy } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import type { ViewState } from './view.js';
	import { evalDrives, rampAt, type MeshState } from '../drives.js';
	import type { SceneKind, Vec3 } from '../scene.js';
	import type { NodeProps } from '../registry.js';

	let {
		value,
		good = true,
		data,
		onbounds,
		onparts,
		onfail,
		label: _label,
		selected: _selected,
		...over
	}: NodeProps & {
		/** The document's kind entry: model + drives. */
		data: SceneKind;
		/** The model's local box, once loaded — the halo and label use it. */
		onbounds?: (b: { size: Vec3; center: Vec3 }) => void;
		/** Each named part's box in the model's frame, once loaded — what a
		 * box-point port or label resolves against. */
		onparts?: (parts: Record<string, { size: Vec3; center: Vec3 }>) => void;
		/** The model could not be loaded; Scene3D falls back to the Svelte kind. */
		onfail?: (err: unknown) => void;
	} = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	const view = getContext<ViewState>('hmi3d:view') ?? { lit: true };
	let lookKey = $derived(view.paper ? 'paper' : view.lit ? 'lit' : 'flat');

	let root = $state<Object3D | null>(null);
	const meshes = new Map<string, Object3D>();
	const baseRot = new Map<Object3D, Euler>();
	/** Materials this node cloned (so a tint on one pump never paints the next). */
	const owned = new Map<Mesh, MeshStandardMaterial>();
	const original = new Map<Mesh, Mesh['material']>();
	/** The look's version of each model material (flat and paper only; lit wears the original). */
	const bases = new Map<Material, MeshStandardMaterial>();
	/** The look the clones in `owned` and `bases` were made for. */
	let painted = '';
	let staleMat: MeshStandardMaterial | undefined;
	let missing = new Set<string>();

	/** What the mesh wears in this look with no drive on it. From the
	 * model's own material, never what the mesh wears now: that may be the
	 * stale grey, and a clone of it would stay grey once good. */
	function baseMaterial(m: Mesh): MeshStandardMaterial | undefined {
		const cur = original.get(m) ?? m.material;
		const src = Array.isArray(cur) ? cur[0] : cur;
		if (!(src instanceof MeshStandardMaterial)) return undefined;
		if (view.lit) return src;
		let b = bases.get(src);
		if (b) return b;
		b = src.clone();
		b.metalness = 0;
		b.roughness = view.paper ? 1 : 0.6;
		b.map = null;
		b.metalnessMap = null;
		b.roughnessMap = null;
		b.normalMap = null;
		b.emissiveMap = null;
		if (view.paper) {
			b.color.set(src.transparent ? palette.fluid : palette.steel);
			b.emissive.set(0);
			b.emissiveIntensity = 0;
		}
		bases.set(src, b);
		return b;
	}

	function ownMaterial(m: Mesh): MeshStandardMaterial | undefined {
		let mat = owned.get(m);
		if (mat) return mat;
		const src = baseMaterial(m);
		if (!src) return undefined;
		mat = src.clone();
		owned.set(m, mat);
		return mat;
	}

	/** Drop the clones made for the last look (or the last model). */
	function dropPaint() {
		for (const m of owned.values()) m.dispose();
		owned.clear();
		for (const b of bases.values()) b.dispose();
		bases.clear();
		painted = '';
	}

	/** A palette slot or a CSS colour. */
	function colour(name: string): string {
		const p = palette as unknown as Record<string, unknown>;
		if (typeof p[name] === 'string') return p[name] as string;
		if (palette.priority[name]) return palette.priority[name];
		return name;
	}

	/** Let go of the previous clone's meshes and the materials cloned for
	 * it (a new model, or unmount); the cached glTF's own are never touched. */
	function release() {
		dropPaint();
		original.clear();
		meshes.clear();
		baseRot.clear();
	}

	$effect(() => {
		const url = data.model;
		if (!url) return;
		let cancelled = false;
		loadModel(url).then(
			(gltf) => {
				if (cancelled) return;
				release();
				const clone = gltf.scene.clone(true);
				clone.traverse((o) => {
					if (o.name) meshes.set(o.name, o);
					if (o instanceof Mesh) {
						o.castShadow = true;
						o.receiveShadow = true;
						original.set(o, o.material);
						// Water and glass take no clicks: a translucent mesh
						// (a well's fluid, a sight glass) is what the eye looks
						// THROUGH, and a pick that stopped at it would hand
						// every click on the pump in the well to the well.
						const mat = Array.isArray(o.material) ? o.material[0] : o.material;
						if (mat && mat.transparent && mat.opacity < 1) o.raycast = () => {};
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
				if (onparts) {
					// The clone has no parent yet, so a world box is a box in the
					// model's own frame — the node's.
					const parts: Record<string, { size: Vec3; center: Vec3 }> = {};
					for (const [name, o] of meshes) {
						const b = new Box3().setFromObject(o);
						if (b.isEmpty()) continue;
						parts[name] = { size: b.getSize(new Vector3()).toArray() as Vec3, center: b.getCenter(new Vector3()).toArray() as Vec3 };
					}
					onparts(parts);
				}
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

	// Apply everything but spin whenever the value (or quality) changes;
	// a change of look drops the clones made for the last one first.
	$effect(() => {
		const r = root;
		const key = lookKey;
		if (!r) return;
		void good;
		if (painted !== key) {
			dropPaint();
			painted = key;
		}
		for (const s of states) apply(s);
		applyQuality();
	});

	/** A ramp's colour at its value: the two stops around it, mixed. */
	function mixed(r: { ramp: [number, string][]; value: number }): Color {
		const { from, to, t } = rampAt(r.ramp, r.value);
		return new Color(colour(from)).lerp(new Color(colour(to)), t);
	}

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
			const srcMat = baseMaterial(o);
			if (s.tint !== undefined) {
				if (s.tint && typeof s.tint === 'object') mat.color.copy(mixed(s.tint));
				else mat.color.set(s.tint ? colour(s.tint) : (srcMat?.color ?? new Color('#888')));
			}
			if (s.emissive !== undefined) {
				if (s.emissive && 'ramp' in s.emissive) {
					mat.emissive.copy(mixed(s.emissive));
					mat.emissiveIntensity = s.emissive.intensity;
				} else if (s.emissive) {
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
	 * model must never look like a healthy one. Good again: the look's paint. */
	function applyQuality() {
		for (const [m, orig] of original) {
			if (!good) {
				staleMat ??= new MeshStandardMaterial({ color: palette.stale, transparent: true, opacity: 0.45, metalness: 0, roughness: 0.8 });
				m.material = staleMat;
			} else m.material = owned.get(m) ?? baseMaterial(m) ?? orig;
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
		release();
		staleMat?.dispose();
	});
</script>

{#if root}
	<T is={root} />
{/if}
