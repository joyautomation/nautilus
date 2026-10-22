<script lang="ts">
	// Inside the <Canvas>: the renderer's Scene3D on the one-node document,
	// the turntable — the node's own group turned a little each frame
	// (PlacedNode.object, the group the scene editor's gizmo moves), so
	// nothing is written anywhere and the document stays as composed — and
	// the part picker: the pointer over the model finds the named object
	// under it, a click picks it (kd.picked, where a new drive starts), and
	// the hovered and picked parts wear a box of their own, so "which part"
	// is a click and not a guess from a list of names.
	import { getContext } from 'svelte';
	import { useTask, useThrelte } from '@threlte/core';
	import { Box3, BoxGeometry, EdgesGeometry, Group, LineBasicMaterial, LineSegments, Matrix4, Mesh, MeshBasicMaterial, Object3D, Raycaster, Vector2, Vector3, type Intersection } from 'three';
	import { DEFAULT_PALETTE, Scene3D, resolvePick, seeThrough, type NodeRegistry, type Palette, type PickHit, type PlacedNode, type SceneDoc } from '@joyautomation/nautilus-hmi-3d';
	import { kd } from './kindState.svelte';

	let { doc, registry, tags }: { doc: SceneDoc; registry: NodeRegistry; tags: Record<string, unknown> } = $props();

	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	// Pointer events go on the canvas's WRAPPER (`dom`): OrbitControls
	// captures the pointer there on press, so the release is delivered to
	// it and never passes through the canvas.
	const { camera, canvas, dom, invalidate } = useThrelte();

	let placed = $state<Record<string, PlacedNode>>({});
	const RAD_PER_S = 0.4;
	useTask((dt) => {
		const obj = placed.PREVIEW?.object;
		if (!obj || !kd.turntable) return;
		obj.rotation.y = (obj.rotation.y + dt * RAD_PER_S) % (Math.PI * 2);
	});

	// ── picking ────────────────────────────────────────────────────────
	// The model's meshes are raycast directly (Mesh.prototype.raycast), not
	// through the scene's picker: the renderer turns raycasting OFF on glass
	// and water so a scene click reaches the pump in the well, but here the
	// sight glass IS a part someone may want a drive on. The hits still
	// order the way the scene's do (resolvePick): the frontmost opaque part
	// wins over the glass in front of it, and a click on the picked part
	// goes one deeper, so a part inside a shell is reachable.
	const raycaster = new Raycaster();
	const ndc = new Vector2();
	const root = $derived(placed.PREVIEW?.object ?? null);

	/** The named object a hit belongs to: the nearest named ancestor the
	 * model lists, or none (the renderer's own cage and hover fill). */
	function partOf(o: Object3D | null): string | null {
		for (let p = o; p && p !== root; p = p.parent) if (p.name && kd.meshes.includes(p.name)) return p.name;
		return null;
	}
	function shown(o: Object3D): boolean {
		for (let p: Object3D | null = o; p && p !== root; p = p.parent) if (!p.visible) return false;
		return true;
	}
	function hitsAt(clientX: number, clientY: number): PickHit[] {
		if (!root) return [];
		const r = canvas.getBoundingClientRect();
		if (!r.width || !r.height) return [];
		ndc.set(((clientX - r.left) / r.width) * 2 - 1, -((clientY - r.top) / r.height) * 2 + 1);
		raycaster.setFromCamera(ndc, camera.current);
		const hits: Intersection[] = [];
		root.traverse((o) => {
			if ((o as Mesh).isMesh && !o.userData.nxHighlight && shown(o)) Mesh.prototype.raycast.call(o as Mesh, raycaster, hits);
		});
		hits.sort((a, b) => a.distance - b.distance);
		return hits.map((h) => ({ node: partOf(h.object) ?? undefined, opaque: !seeThrough(h.object) }));
	}

	let down: { x: number; y: number } | null = null;
	const DRAG_PX = 4;
	function onMove(e: PointerEvent) {
		if (e.buttons) return; // orbiting: the hover holds
		const h = resolvePick(hitsAt(e.clientX, e.clientY), null);
		if (h !== kd.hover) kd.hover = h;
	}
	function onDown(e: PointerEvent) {
		down = e.button === 0 ? { x: e.clientX, y: e.clientY } : null;
	}
	function onUp(e: PointerEvent) {
		const d = down;
		down = null;
		if (!d || Math.hypot(e.clientX - d.x, e.clientY - d.y) > DRAG_PX) return;
		kd.picked = resolvePick(hitsAt(e.clientX, e.clientY), kd.picked);
	}
	function onLeave() {
		if (kd.hover) kd.hover = null;
	}
	// The cursor says a part is under the pointer (on the canvas itself:
	// OrbitControls writes the wrapper's).
	$effect(() => {
		canvas.style.cursor = kd.hover ? 'pointer' : '';
		return () => {
			canvas.style.cursor = '';
		};
	});
	$effect(() => {
		dom.addEventListener('pointermove', onMove);
		dom.addEventListener('pointerdown', onDown);
		dom.addEventListener('pointerup', onUp);
		dom.addEventListener('pointerleave', onLeave);
		return () => {
			dom.removeEventListener('pointermove', onMove);
			dom.removeEventListener('pointerdown', onDown);
			dom.removeEventListener('pointerup', onUp);
			dom.removeEventListener('pointerleave', onLeave);
		};
	});

	// ── the highlight ──────────────────────────────────────────────────
	// A box in the part's own frame, added as its child so it turns, scales
	// and hides with the part. Edges on top (no depth test: a part inside a
	// shell still shows where it is), a faint fill behind.
	function findPart(name: string): Object3D | null {
		let found: Object3D | null = null;
		root?.traverse((o) => {
			if (!found && o.name === name) found = o;
		});
		return found;
	}
	/** The part's box in its own frame: every mesh under it, each one's
	 * geometry box carried from the mesh's frame into the part's. */
	function localBox(part: Object3D): Box3 {
		part.updateWorldMatrix(true, true);
		const inv = part.matrixWorld.clone().invert();
		const box = new Box3();
		const m = new Matrix4();
		part.traverse((o) => {
			const mesh = o as Mesh;
			if (!mesh.isMesh || o.userData.nxHighlight || !mesh.geometry) return;
			if (!mesh.geometry.boundingBox) mesh.geometry.computeBoundingBox();
			const b = mesh.geometry.boundingBox!.clone();
			b.applyMatrix4(m.multiplyMatrices(inv, mesh.matrixWorld));
			box.union(b);
		});
		return box;
	}
	function makeHighlight(part: Object3D, color: string, fill: number): Group | null {
		const box = localBox(part);
		if (box.isEmpty()) return null;
		const size = new Vector3();
		const center = new Vector3();
		box.getSize(size);
		box.getCenter(center);
		const pad = 1.04;
		const geo = new BoxGeometry(size.x * pad || 0.001, size.y * pad || 0.001, size.z * pad || 0.001);
		const g = new Group();
		g.userData.nxHighlight = true;
		const edges = new LineSegments(new EdgesGeometry(geo), new LineBasicMaterial({ color, depthTest: false, transparent: true, opacity: 0.95 }));
		const fillMesh = new Mesh(geo, new MeshBasicMaterial({ color, transparent: true, opacity: fill, depthWrite: false }));
		for (const o of [edges, fillMesh]) {
			o.position.copy(center);
			o.raycast = () => {};
			o.userData.nxHighlight = true;
			o.userData.ink = false;
			o.renderOrder = 999;
			g.add(o);
		}
		part.add(g);
		return g;
	}
	function drop(g: Group | null) {
		if (!g) return;
		g.removeFromParent();
		g.traverse((o) => {
			const m = o as Mesh;
			m.geometry?.dispose?.();
			(m.material as { dispose?: () => void } | undefined)?.dispose?.();
		});
	}
	let hoverBox: Group | null = null;
	let pickBox: Group | null = null;
	$effect(() => {
		const name = kd.picked;
		void kd.modelState;
		void root;
		drop(pickBox);
		pickBox = null;
		const part = name ? findPart(name) : null;
		if (part) pickBox = makeHighlight(part, palette.selected, 0.12);
		invalidate();
		return () => {
			drop(pickBox);
			pickBox = null;
		};
	});
	$effect(() => {
		const name = kd.hover;
		void kd.modelState;
		void root;
		drop(hoverBox);
		hoverBox = null;
		const part = name && name !== kd.picked ? findPart(name) : null;
		if (part) hoverBox = makeHighlight(part, palette.hover, 0.08);
		invalidate();
		return () => {
			drop(hoverBox);
			hoverBox = null;
		};
	});

	// Harness hook: what is on stage, read-only, and where a part is on
	// the page so a test can put the pointer on it.
	$effect(() => {
		const w = window as unknown as { __nxKind?: unknown };
		w.__nxKind = {
			rotation: () => placed.PREVIEW?.object?.rotation.y ?? null,
			hover: () => kd.hover,
			picked: () => kd.picked,
			partAt: (name: string) => {
				const part = findPart(name);
				if (!part) return null;
				const box = localBox(part);
				if (box.isEmpty()) return null;
				const c = new Vector3();
				box.getCenter(c);
				part.localToWorld(c);
				c.project(camera.current);
				const r = canvas.getBoundingClientRect();
				return { x: r.left + ((c.x + 1) / 2) * r.width, y: r.top + ((1 - c.y) / 2) * r.height };
			}
		};
		return () => {
			delete w.__nxKind;
		};
	});
</script>

<Scene3D {doc} {registry} {tags} look={kd.look} pick={false} bind:placed />
