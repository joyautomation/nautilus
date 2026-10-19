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
	import { Vector3 } from 'three';
	import type { TransformControls as TransformControlsImpl } from 'three/examples/jsm/controls/TransformControls.js';
	import { Scene3D, type NodeRegistry, type PlacedNode, type SceneDoc, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import { ROT_SNAP_DEG, disarm, ed, groundY, postOp, snapCell, snapPos } from './sceneState.svelte';

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
			size: () => ({ w: size.current.width, h: size.current.height })
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
		if (ed.tool === 'place' && ed.placeKind) {
			const p = e.point;
			postOp({ type: 'addNode', kind: ed.placeKind, pos: snapPos([p.x, ground, p.z], e.nativeEvent.shiftKey) });
			disarm();
			return;
		}
		ed.selected = null;
	}

	function onPick(id: string) {
		if (ed.tool === 'place') return;
		ed.selected = id;
	}

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
		postOp({ type: 'moveNode', id, pos, rot });
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
	{/if}
</Scene3D>
