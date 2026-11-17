<script lang="ts">
	// A rectangle of lettering, w x h mm, lying in its own XY plane and
	// centred on `position`. Redrawn when `items` change (a live value).
	import { T, useThrelte } from '@threlte/core';
	import { onDestroy } from 'svelte';
	import { CanvasTexture, SRGBColorSpace } from 'three';
	import { drawPrint, type Ink, type Paint, type Plate } from './print';

	let {
		w,
		h,
		items,
		plate,
		paint,
		res = 10,
		position = [0, 0, 0],
		rotation = [0, 0, 0]
	}: {
		w: number;
		h: number;
		items: Ink[];
		plate?: Plate;
		paint?: Paint;
		/** Pixels per mm. */
		res?: number;
		position?: [number, number, number];
		rotation?: [number, number, number];
	} = $props();

	const { invalidate } = useThrelte();
	const canvas = document.createElement('canvas');
	const map = new CanvasTexture(canvas);
	map.colorSpace = SRGBColorSpace;
	map.anisotropy = 8;
	$effect(() => {
		drawPrint(canvas, w, h, items, res, plate, paint);
		map.needsUpdate = true;
		invalidate();
	});
	onDestroy(() => map.dispose());
</script>

<T.Mesh {position} {rotation} renderOrder={2}>
	<T.PlaneGeometry args={[w, h]} />
	<T.MeshBasicMaterial {map} transparent toneMapped={false} depthWrite={false} />
</T.Mesh>
