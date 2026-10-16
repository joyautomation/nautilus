<script lang="ts" module>
	// A production well as a KIND (docs/design/spatial-hmi.md §3d): the
	// ground cut away at one corner so the casing, the water in it and the
	// submersible pump at the bottom are in view — the part of a well
	// screen a P&ID cannot show. It reads ONE struct tag of the site's
	// `Well` UDT; well.scene.json names this file under kinds.well.component
	// and places WL101 as one node.
	import { member, num, type KindMeta } from '@joyautomation/nautilus-hmi-3d';

	export const kind: KindMeta = {
		type: 'Well',
		members: ['Pump', 'Depth', 'Static', 'Flow', 'LL'],
		bounds: { size: [1.3, 2.1, 1.3], center: [0, -0.65, 0] },
		labelAt: [0, 0.75, 0],
		status: (v, good) => {
			if (!good) return 'stale';
			const run = member(member(v, 'Pump'), 'Running') === true;
			return `${run ? `${num(member(v, 'Flow')).toFixed(1)} L/s` : 'stopped'} · ${num(member(v, 'Depth')).toFixed(2)} m`;
		}
	};
</script>

<script lang="ts">
	// The ground is an L-shaped block (the notch at the camera's corner is
	// the cutaway); the casing is three quarters of a cylinder with the
	// open quarter facing the notch; the water column is drawn to the
	// pumping level and a thin ring marks the static level. The pump is
	// the built-in, as a part reading `Pump`, hung at the bottom on its
	// riser; the riser's flow is the pump's.
	import { T } from '@threlte/core';
	import { getContext } from 'svelte';
	import { Node, Pipe, DEFAULT_PALETTE, type NodeProps, type Palette } from '@joyautomation/nautilus-hmi-3d';

	let { value, good = true }: NodeProps = $props();
	// The look's colours (paper lays its near-whites over the theme's greys).
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;

	const DEPTH = 1.6; // the drawing's hole, metres
	const R = 0.13; // casing
	let depth = $derived(Math.min(DEPTH - 0.1, Math.max(0.1, num(member(value, 'Depth'), 0.5))));
	let stat = $derived(Math.min(DEPTH - 0.1, Math.max(0.1, num(member(value, 'Static'), 0.5))));
	let waterH = $derived(DEPTH - depth);
	let water = $derived(good ? palette.fluid : palette.stale);
	let steel = $derived(good ? palette.steel : palette.stale);
</script>

<!-- the ground, cut away at the +x +z corner -->
<T.Mesh position={[0, -DEPTH / 2, -0.25]}>
	<T.BoxGeometry args={[1.2, DEPTH, 0.7]} />
	<T.MeshStandardMaterial color={palette.steelDark} metalness={0} roughness={0.9} />
</T.Mesh>
<T.Mesh position={[-0.25, -DEPTH / 2, 0.35]}>
	<T.BoxGeometry args={[0.7, DEPTH, 0.5]} />
	<T.MeshStandardMaterial color={palette.steelDark} metalness={0} roughness={0.9} />
</T.Mesh>

<!-- the casing: open toward the notch -->
<T.Mesh position={[0.1, -DEPTH / 2, 0.1]}>
	<T.CylinderGeometry args={[R, R, DEPTH, 48, 1, true, Math.PI / 2, (3 * Math.PI) / 2]} />
	<T.MeshStandardMaterial color={palette.shell} metalness={0} roughness={0.6} side={2} />
</T.Mesh>
<!-- the water in it, up to the pumping level -->
<T.Mesh position={[0.1, -DEPTH + waterH / 2, 0.1]}>
	<T.CylinderGeometry args={[R - 0.01, R - 0.01, waterH, 48]} />
	<T.MeshStandardMaterial color={water} transparent opacity={0.9} metalness={0} roughness={0.5} />
</T.Mesh>
<!-- the static level: a ring on the casing -->
<T.Mesh position={[0.1, -stat, 0.1]} userData={{ ink: false }}>
	<T.TorusGeometry args={[R + 0.004, 0.004, 6, 48]} rotation={[Math.PI / 2, 0, 0]} />
	<T.MeshBasicMaterial color={palette.ink} />
</T.Mesh>

<!-- the wellhead: a pedestal on the slab, the discharge tee off it -->
<T.Mesh position={[0.1, 0.03, 0.1]}>
	<T.CylinderGeometry args={[R + 0.05, R + 0.05, 0.06, 48]} />
	<T.MeshStandardMaterial color={steel} metalness={0} roughness={0.6} />
</T.Mesh>
<T.Mesh position={[0.1, 0.22, 0.1]}>
	<T.BoxGeometry args={[0.16, 0.32, 0.16]} />
	<T.MeshStandardMaterial color={steel} metalness={0} roughness={0.6} />
</T.Mesh>

<!-- the submersible: the built-in pump, stood on end at the bottom of
     the hole, and its riser up the casing to the wellhead -->
<Node id="pump" tag="Pump" kind="pump" pos={[0.1, -DEPTH + 0.2, 0.1]} rot={[0, 0, -90]} scale={0.55} />
<Pipe points={[[0.1, -DEPTH + 0.26, 0.1], [0.1, 0.06, 0.1]]} radius={0.018} flowing="Pump.Running" />
<Pipe points={[[0.1, 0.35, 0.1], [0.1, 0.35, 0.0], [0.17, 0.35, 0.0]]} radius={0.02} flowing="Pump.Running" arrows={false} />
