<script lang="ts">
	// Fade whatever is inside: every material's opacity times `amount`, and
	// below `pickable` nothing inside takes a pick — so a device in the
	// background of a focused view can be seen but not hit by accident.
	// Materials are restored exactly when the fade lifts. Children mount
	// late (a glTF part loads after its node) and write their own materials
	// (a part repaints its LED, tint and opacity when its data changes), so
	// while faded the subtree is re-swept every frame, before it is drawn:
	// swept less often, a repainted part shows at full opacity until the
	// next sweep — a flicker. A value a part wrote is its new own opacity.
	// Unfaded it costs nothing.
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
		/** What the fade last wrote, to tell a part's own write from it. */
		wrote: number;
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
					s = { opacity: m.opacity, transparent: m.transparent, depthWrite: m.depthWrite, wrote: NaN };
					m.userData[SAVED] = s;
				} else if (m.opacity !== s.wrote) {
					// the part repainted itself: that is its own opacity now
					s.opacity = m.opacity;
					s.transparent = m.transparent;
					s.depthWrite = m.depthWrite;
				}
				const want = s.opacity * k;
				if (m.opacity !== want || !m.transparent || m.depthWrite) {
					const recompile = !m.transparent;
					m.opacity = want;
					m.transparent = true;
					m.depthWrite = false;
					if (recompile) m.needsUpdate = true;
				}
				s.wrote = want;
			}
		});
	}

	let last = 1;
	useTask(() => {
		if (!group) return;
		// Sweep on every change, then every frame while faded.
		if (amount !== last || amount < 1) {
			apply(group, amount, amount >= pickable);
			last = amount;
		}
	});
</script>

<T.Group bind:ref={group}>
	{@render children()}
</T.Group>
