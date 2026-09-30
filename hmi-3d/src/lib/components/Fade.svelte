<script lang="ts">
	// Fade whatever is inside: every material's opacity times `amount`, and
	// below `pickable` nothing inside takes a pick — so a device in the
	// background of a focused view can be seen but not hit by accident.
	// Materials are restored exactly when the fade lifts. Children mount
	// late (a glTF part loads after its node), so while faded the subtree is
	// re-swept a few times a second; unfaded it costs nothing.
	import { T, useTask } from '@threlte/core';
	import { Mesh, type Group, type Material, type Object3D } from 'three';
	import type { Snippet } from 'svelte';

	let { amount = 1, pickable = 0.5, children }: { amount?: number; pickable?: number; children: Snippet } = $props();

	let group = $state<Group>();
	const noRaycast = () => {};
	interface Saved {
		opacity: number;
		transparent: boolean;
		depthWrite: boolean;
	}
	const SAVED = 'fade:saved';

	function apply(root: Object3D, k: number, hit: boolean) {
		root.traverse((o) => {
			if (o instanceof Mesh) {
				if (!hit && o.raycast !== noRaycast) {
					o.userData['fade:raycast'] = o.raycast;
					o.raycast = noRaycast;
				} else if (hit && o.userData['fade:raycast']) {
					o.raycast = o.userData['fade:raycast'];
					delete o.userData['fade:raycast'];
				}
			}
			const mat = (o as Mesh).material as Material | Material[] | undefined;
			if (!mat) return;
			for (const m of Array.isArray(mat) ? mat : [mat]) {
				let s = m.userData[SAVED] as Saved | undefined;
				if (k >= 1) {
					if (s) {
						m.opacity = s.opacity;
						m.transparent = s.transparent;
						m.depthWrite = s.depthWrite;
						delete m.userData[SAVED];
						m.needsUpdate = true;
					}
					continue;
				}
				if (!s) {
					s = { opacity: m.opacity, transparent: m.transparent, depthWrite: m.depthWrite };
					m.userData[SAVED] = s;
				}
				const want = s.opacity * k;
				if (m.opacity !== want || !m.transparent) {
					m.opacity = want;
					m.transparent = true;
					m.depthWrite = false;
					m.needsUpdate = true;
				}
			}
		});
	}

	let since = 0;
	let last = 1;
	useTask((dt) => {
		if (!group) return;
		since += dt;
		// Sweep on every change, then while faded four times a second.
		if (amount !== last || (amount < 1 && since > 0.25)) {
			apply(group, amount, amount >= pickable);
			last = amount;
			since = 0;
		}
	});
</script>

<T.Group bind:ref={group}>
	{@render children()}
</T.Group>
