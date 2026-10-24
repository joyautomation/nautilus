<script lang="ts">
	// Inside the <Canvas>: the renderer's Scene3D, a ground plane that takes
	// the clicks nothing else does (a drop in place mode, a deselect
	// otherwise), and the gizmo on the selected node. The gizmo is three's
	// TransformControls on the node's own group (PlacedNode.object) — the
	// node moves live under the pointer, and on release its position and
	// rotation are posted as ONE moveNode op, which the host writes to the
	// text and echoes back; the group is then where the document says.
	import { T, useThrelte, useTask } from '@threlte/core';
	import { TransformControls, useOrbitControls } from '@threlte/extras';
	import type { IntersectionEvent } from '@threlte/extras';
	import { Group, Mesh, MeshBasicMaterial, Raycaster, SphereGeometry, Vector2, Vector3, type Intersection } from 'three';
	import type { TransformControls as TransformControlsImpl } from 'three/examples/jsm/controls/TransformControls.js';
	import { Scene3D, docPorts, portInFrame, routePorts, type KindBoxes, type NodeRegistry, type PipeAnchor, type PlacedNode, type SceneBox, type SceneDoc, type ScenePort, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import { ROT_SNAP_DEG, disarm, ed, groundY, postOp, selectPipe, snapCell, snapPos } from './sceneState.svelte';
	import { dropMark, groupBox, layPortMarks, partBoxes, portOf, type PortMark } from './marks';

	let { doc, registry }: { doc: SceneDoc; registry: NodeRegistry } = $props();

	let placed = $state<Record<string, PlacedNode>>({});
	const selected = $derived(ed.selected ? (placed[ed.selected]?.object ?? undefined) : undefined);
	const ground = $derived(groundY(doc));
	const tags = $derived(ed.tags ?? {});

	const DRAG_PX = 5;
	/** Shift held at the last pointer-down on the canvas: a free (unsnapped)
	 * drag, the mimic's convention. */
	let shift = $state(false);
	const { dom, camera, size } = useThrelte();
	const orbit = useOrbitControls();
	let controls = $state<TransformControlsImpl>();

	// A small window surface for the gesture harness (gesture-harness/
	// scene.test.mjs), which drives this bundle with real pointer input and
	// needs to aim at things that live in 3D: where a world point lands on
	// the canvas, and which gizmo handle the pointer is over. Read-only.
	$effect(() => {
		const w = window as unknown as { __nxScene?: unknown };
		w.__nxScene = {
			project: (x: number, y: number, z: number): [number, number] => {
				const v = new Vector3(x, y, z).project(camera.current);
				const r = dom.getBoundingClientRect();
				return [r.left + ((v.x + 1) / 2) * r.width, r.top + ((1 - v.y) / 2) * r.height];
			},
			gizmoAxis: () => controls?.axis ?? null,
			size: () => ({ w: size.current.width, h: size.current.height }),
			ports: () => {
				const r = dom.getBoundingClientRect();
				const out: { node: string; port: string; x: number; y: number }[] = [];
				for (const [node, m] of marks)
					for (const [port, mark] of m) {
						const c = mark.getWorldPosition(new Vector3()).project(camera.current);
						out.push({ node, port, x: r.left + ((c.x + 1) / 2) * r.width, y: r.top + ((1 - c.y) / 2) * r.height });
					}
				return out;
			},
			pipeFrom: () => ed.pipeFrom,
			portAt: (x: number, y: number) => portAt(x, y),
			vertices: () => {
				const r = dom.getBoundingClientRect();
				return handles.children.map((h, i) => {
					const c = h.getWorldPosition(new Vector3()).project(camera.current);
					return { index: h.userData.nxVertex as number, selected: ed.selectedVertex === h.userData.nxVertex, x: r.left + ((c.x + 1) / 2) * r.width, y: r.top + ((1 - c.y) / 2) * r.height, i };
				});
			},
			selectedPipe: () => ed.selectedPipe,
			selectedVertex: () => ed.selectedVertex
		};
		return () => {
			delete w.__nxScene;
		};
	});
	$effect(() => {
		const el = dom;
		const down = (e: PointerEvent) => {
			shift = e.shiftKey;
		};
		el.addEventListener('pointerdown', down);
		return () => el.removeEventListener('pointerdown', down);
	});

	function onGround(e: IntersectionEvent<MouseEvent>) {
		if (e.delta > DRAG_PX) return;
		// A click that landed on a vertex handle or a port is not the ground's.
		if (portClick) return;
		if (ed.tool === 'place' && ed.placeKind) {
			const p = e.point;
			postOp({ type: 'addNode', kind: ed.placeKind, pos: snapPos([p.x, ground, p.z], e.nativeEvent.shiftKey) });
			disarm();
			return;
		}
		ed.selected = null;
		ed.pipeFrom = null;
		selectPipe(null);
	}

	function onPick(id: string) {
		if (ed.tool === 'place') return;
		// A click that landed on a port or a vertex handle is not a pick.
		if (portClick) return;
		ed.selected = id;
		selectPipe(null);
	}

	// ── a pipe's points as handles (§3g slice 3: vertex drag) ──────────
	// The selected pipe's own points — not its anchored ends, which belong
	// to their nodes — wear a handle each; a click on one puts the gizmo
	// on it, and release posts ONE setPipePoints with the point moved.
	const handles = new Group();
	handles.userData = { nxHighlight: true, ink: false };
	const HANDLE_R = 0.02;
	const handleGeo = new SphereGeometry(HANDLE_R, 12, 8);
	let vertexObj = $state<Mesh | undefined>(undefined);
	$effect(() => {
		const i = ed.selectedPipe;
		const v = ed.selectedVertex;
		const pipe = i !== null ? doc.pipes?.[i] : undefined;
		for (const h of [...handles.children]) {
			handles.remove(h);
			((h as Mesh).material as MeshBasicMaterial).dispose();
		}
		vertexObj = undefined;
		if (!pipe) return;
		(pipe.points ?? []).forEach((p, k) => {
			const hot = k === v;
			const m = new Mesh(handleGeo, new MeshBasicMaterial({ color: hot ? '#ffe08a' : '#4ea1ff', depthTest: false, transparent: true, opacity: hot ? 1 : 0.9 }));
			m.userData = { nxVertex: k, nxHighlight: true, ink: false };
			m.renderOrder = 999;
			m.position.set(...p);
			m.scale.setScalar(hot ? 1.4 : 1);
			handles.add(m);
			if (hot) vertexObj = m;
		});
	});
	/** The handle under a canvas point, or null. */
	function vertexAt(clientX: number, clientY: number): number | null {
		if (!handles.children.length) return null;
		const r = dom.getBoundingClientRect();
		if (!r.width || !r.height) return null;
		ndc.set(((clientX - r.left) / r.width) * 2 - 1, -((clientY - r.top) / r.height) * 2 + 1);
		raycaster.setFromCamera(ndc, camera.current);
		const hits = raycaster.intersectObjects(handles.children, false);
		return hits.length ? (hits[0].object.userData.nxVertex as number) : null;
	}
	/** The vertex gizmo released: the point where the handle now is. */
	function onVertexDrop() {
		const i = ed.selectedPipe;
		const v = ed.selectedVertex;
		const obj = vertexObj;
		const pipe = i !== null ? ed.doc?.pipes?.[i] : undefined;
		if (!pipe || v === null || !obj) return;
		const p = obj.position;
		const pos = snapPos([p.x, p.y, p.z], shift);
		const was = pipe.points?.[v];
		const mm = (x: number) => Math.round(x * 1000);
		if (was && pos.every((c, k) => mm(c) === mm(was[k]))) return;
		const points = (pipe.points ?? []).map((q, k) => (k === v ? pos : q));
		postOp({ type: 'setPipePoints', index: i!, points });
	}

	// ── a node moved: its auto-routed pipes follow ──────────────────────
	/** The pipes anchored to `id` at both ends whose run is still the one
	 * routePorts drew (nobody moved a point): they are routed again from
	 * the node's new place, in the same op as the move. A run someone
	 * tweaked is left alone — its first leg stretches, as a hand-laid pipe
	 * should. */
	function reroutedPipes(id: string, moved: Pick<SceneDoc['nodes'][number], 'pos' | 'rot' | 'scale'>): { index: number; points: Vec3[] }[] {
		const out: { index: number; points: Vec3[] }[] = [];
		const same = (a: Vec3[], b: Vec3[]) => a.length === b.length && a.every((p, k) => p.every((c, j) => Math.abs(c - b[k][j]) < 1e-6));
		(doc.pipes ?? []).forEach((pipe, index) => {
			if (!pipe.from || !pipe.to || (pipe.from.node !== id && pipe.to.node !== id)) return;
			const a = endOf(pipe.from);
			const b = endOf(pipe.to);
			if (!a || !b || !same(pipe.points ?? [], routePorts(a, b))) return;
			const a2 = pipe.from.node === id ? endOf(pipe.from, moved) : a;
			const b2 = pipe.to.node === id ? endOf(pipe.to, moved) : b;
			if (a2 && b2) out.push({ index, points: routePorts(a2, b2) });
		});
		return out;
	}

	// ── ports and pipes (§3g slice 3) ──────────────────────────────────
	// Every node's ports wear a mark under the node's own group, so they
	// follow the gizmo live. A click on a port starts a pipe; a click on
	// another port ends it: ONE addPipe anchored at both ends, with the
	// orthogonal route between them as its points. Esc or the ground
	// cancels. The marks are raycast here (the renderer's picker knows
	// nodes, not ports), and a click that hits a mark is kept from the
	// node pick behind it.
	const portsOf = (kind: string): Record<string, ScenePort> | undefined => docPorts(doc, kind) ?? registry[kind]?.ports;
	/** What a node's box-point ports resolve against: the kind's declared
	 * bounds and the parts under the node's group — present once the
	 * renderer has the model on stage. */
	function boxesOf(n: SceneDoc['nodes'][number]): KindBoxes {
		const group = placed[n.id]?.object;
		const parts = group ? partBoxes(group) : {};
		const entry = doc.kinds?.[n.kind];
		const declared = entry && typeof entry === 'object' ? entry.bounds : undefined;
		const fallback = registry[n.kind]?.bounds;
		const b: SceneBox | undefined = declared && declared !== 'auto' ? declared : fallback && fallback !== 'auto' ? fallback : group ? groupBox(group) : undefined;
		return { bounds: b, parts };
	}
	let marks = new Map<string, Map<string, PortMark>>();
	/** Nodes with a port on a box not yet on stage: laid again as the model arrives. */
	const pending = new Set<string>();
	function layNode(n: SceneDoc['nodes'][number]) {
		const group = placed[n.id]?.object;
		if (!group) return;
		const hot = ed.pipeFrom;
		const hover = ed.hoverPort;
		const hotName = hot?.node === n.id ? hot.port : hover?.node === n.id ? hover.port : null;
		const ports = portsOf(n.kind);
		const laid = layPortMarks(group, ports, boxesOf(n), marks.get(n.id) ?? new Map(), hotName, 1 / (n.scale ?? 1));
		marks.set(n.id, laid);
		if (Object.keys(ports ?? {}).length > laid.size) pending.add(n.id);
		else pending.delete(n.id);
	}
	$effect(() => {
		void ed.pipeFrom;
		void ed.hoverPort;
		for (const n of doc.nodes) layNode(n);
		for (const [id, m] of marks)
			if (!doc.nodes.some((n) => n.id === id)) {
				for (const mark of m.values()) dropMark(mark);
				marks.delete(id);
				pending.delete(id);
			}
	});
	let retryAt = 0;
	useTask(() => {
		if (!pending.size) return;
		const now = performance.now();
		if (now < retryAt) return;
		retryAt = now + 300;
		for (const id of [...pending]) {
			const n = doc.nodes.find((x) => x.id === id);
			if (n) layNode(n);
		}
	});
	const raycaster = new Raycaster();
	const ndc = new Vector2();
	function portAt(clientX: number, clientY: number): PipeAnchor | null {
		const r = dom.getBoundingClientRect();
		if (!r.width || !r.height) return null;
		ndc.set(((clientX - r.left) / r.width) * 2 - 1, -((clientY - r.top) / r.height) * 2 + 1);
		raycaster.setFromCamera(ndc, camera.current);
		const hits: Intersection[] = [];
		for (const m of marks.values()) for (const mark of m.values()) raycaster.intersectObject(mark, true, hits);
		hits.sort((a, b) => a.distance - b.distance);
		for (const h of hits) {
			const port = portOf(h.object);
			if (!port) continue;
			for (const [id, m] of marks) if ([...m.values()].some((mark) => mark === h.object || mark.children.includes(h.object))) return { node: id, port };
		}
		return null;
	}
	let portDown: { x: number; y: number } | null = null;
	/** Set by the pointerup that hit a port, read by the renderer's pick
	 * (its click) right after, so the node under the port is not picked. */
	let portClick = false;
	let hoverVertex = $state<number | null>(null);
	function onPortMove(e: PointerEvent) {
		if (e.buttons) return;
		const p = portAt(e.clientX, e.clientY);
		const cur = ed.hoverPort;
		if ((p?.node ?? null) !== (cur?.node ?? null) || (p?.port ?? null) !== (cur?.port ?? null)) ed.hoverPort = p;
		const v = vertexAt(e.clientX, e.clientY);
		if (v !== hoverVertex) hoverVertex = v;
	}
	/** On the window, capture phase: a port at the top of a node sits under
	 * the node's label, an HTML overlay whose own pointerdown stops at it
	 * (so a drag on a label does not orbit) — a listener on the canvas's
	 * wrapper would never see the press. */
	function onPortDown(e: PointerEvent) {
		if (!dom.contains(e.target as Node)) return;
		portDown = e.button === 0 ? { x: e.clientX, y: e.clientY } : null;
		portClick = false;
	}
	function onPortUp(e: PointerEvent) {
		const d = portDown;
		portDown = null;
		if (!d || Math.hypot(e.clientX - d.x, e.clientY - d.y) > DRAG_PX || ed.tool === 'place') return;
		// A vertex handle first: it sits on the pipe, which sits near ports.
		const v = vertexAt(e.clientX, e.clientY);
		if (v !== null) {
			portClick = true;
			setTimeout(() => (portClick = false), 0);
			ed.selectedVertex = v;
			return;
		}
		const p = portAt(e.clientX, e.clientY);
		if (!p) return;
		portClick = true;
		setTimeout(() => (portClick = false), 0);
		const from = ed.pipeFrom;
		if (!from) {
			ed.pipeFrom = p;
			ed.selected = null;
			return;
		}
		if (from.node === p.node && from.port === p.port) {
			ed.pipeFrom = null;
			return;
		}
		const a = endOf(from);
		const b = endOf(p);
		if (a && b) postOp({ type: 'addPipe', from, to: p, points: routePorts(a, b) });
		ed.pipeFrom = null;
	}
	function endOf(a: PipeAnchor, moved?: Pick<SceneDoc['nodes'][number], 'pos' | 'rot' | 'scale'>): { at: Vec3; dir: Vec3 } | null {
		const n = doc.nodes.find((x) => x.id === a.node);
		const port = n && portsOf(n.kind)?.[a.port];
		return n && port ? portInFrame(moved ? { ...n, ...moved } : n, port, boxesOf(n)) : null;
	}
	$effect(() => {
		dom.addEventListener('pointermove', onPortMove);
		window.addEventListener('pointerdown', onPortDown, true);
		dom.addEventListener('pointerup', onPortUp);
		return () => {
			dom.removeEventListener('pointermove', onPortMove);
			window.removeEventListener('pointerdown', onPortDown, true);
			dom.removeEventListener('pointerup', onPortUp);
		};
	});
	$effect(() => {
		const el = dom.querySelector('canvas') as HTMLElement | null;
		if (el) el.style.cursor = hoverVertex !== null ? 'grab' : ed.hoverPort ? 'crosshair' : '';
	});

	const deg = 180 / Math.PI;
	/** The gizmo released: write where the node now is — unless that is
	 * where the document already has it (a click on a handle without a
	 * drag), which would be an empty edit on the undo stack. The host
	 * rounds pos to the millimetre and rot to a tenth of a degree. */
	function onDrop() {
		const obj = selected;
		const id = ed.selected;
		if (!obj || !id) return;
		const p = obj.position;
		const r = obj.rotation;
		const pos = snapPos([p.x, p.y, p.z], shift);
		const rot: Vec3 = [r.x * deg, r.y * deg, r.z * deg];
		const was = ed.doc?.nodes.find((n) => n.id === id);
		if (was) {
			const mm = (v: number) => Math.round(v * 1000);
			const dd = (v: number) => Math.round(v * 10);
			const wasRot = was.rot ?? [0, 0, 0];
			if (pos.every((v, i) => mm(v) === mm(was.pos[i])) && rot.every((v, i) => dd(v) === dd(wasRot[i]))) return;
		}
		const move = { type: 'moveNode' as const, id, pos, rot };
		const follow = reroutedPipes(id, { pos, rot, scale: was?.scale });
		if (follow.length) postOp({ type: 'batch', ops: [move, ...follow.map((f) => ({ type: 'setPipePoints' as const, index: f.index, points: f.points }))] });
		else postOp(move);
	}

	// The camera as it stands, for "Save view": read each frame it moves.
	useTask(() => {
		const cam = camera.current;
		const target = orbit.current?.target;
		if (!cam || !target) return;
		const v = ed.view;
		if (
			v &&
			v.pos[0] === cam.position.x && v.pos[1] === cam.position.y && v.pos[2] === cam.position.z &&
			v.target[0] === target.x && v.target[1] === target.y && v.target[2] === target.z
		)
			return;
		ed.view = { pos: [cam.position.x, cam.position.y, cam.position.z], target: [target.x, target.y, target.z] };
	});
</script>

<Scene3D {doc} {registry} {tags} selected={ed.selected} look={ed.look ?? undefined} onpick={onPick} bind:placed>
	<!-- The ground: invisible to the eye, opaque to the raycaster (a
	     material with visible=false is skipped by three's Mesh.raycast, so
	     it is transparent instead). -->
	<T.Mesh position={[0, ground - 0.0005, 0]} rotation.x={-Math.PI / 2} onclick={onGround}>
		<T.PlaneGeometry args={[400, 400]} />
		<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
	</T.Mesh>

	<T is={handles} />

	{#if selected}
		<TransformControls
			bind:controls
			object={selected}
			mode={ed.gizmo}
			translationSnap={ed.snapToGrid && !shift ? snapCell() : null}
			rotationSnap={ed.snapToGrid && !shift ? (ROT_SNAP_DEG * Math.PI) / 180 : null}
			size={0.8}
			onmouseUp={onDrop}
		/>
	{:else if vertexObj}
		<TransformControls
			bind:controls
			object={vertexObj}
			mode="translate"
			translationSnap={ed.snapToGrid && !shift ? snapCell() : null}
			size={0.6}
			onmouseUp={onVertexDrop}
		/>
	{/if}
</Scene3D>
