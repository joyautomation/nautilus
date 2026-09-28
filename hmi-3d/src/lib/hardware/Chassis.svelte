<script lang="ts">
	// The chassis a profile describes: floor, side walls, front ears, the
	// board plate and the drive backplane, a removable lid, and the QR label
	// where the profile's `qr` anchor puts it. Metres, in the profile's
	// frame (origin at the bottom centre of the front face, +z out of the
	// front). Only the floor and an opaque lid take clicks — a wall seen
	// edge-on must not steal a pick from the part behind it, and in x-ray
	// nothing of the shell does.
	import { T } from '@threlte/core';
	import { Mesh } from 'three';
	import { mm, type ChassisProfile } from './profile.js';
	import type { Vec3 } from '../scene.js';

	let {
		profile,
		lid = 'on',
		xray = false,
		lidOffset = [0, 0, 0],
		anchor = true,
		good = true
	}: {
		profile: ChassisProfile;
		lid?: 'on' | 'off';
		xray?: boolean;
		/** The lid's exploded offset, metres. */
		lidOffset?: Vec3;
		/** Draw the QR label at the profile's `qr` anchor. */
		anchor?: boolean;
		good?: boolean;
	} = $props();

	const noRaycast = () => {};
	let S = $derived(mm(profile.size));
	let W = $derived(S[0]);
	let H = $derived(S[1]);
	let D = $derived(S[2]);
	let wall = $derived((profile.shell?.wall ?? 1) / 1000);
	let floorT = $derived((profile.shell?.floor ?? 1) / 1000);
	let lidT = $derived((profile.shell?.lid ?? 1) / 1000);
	let ear = $derived((profile.shell?.bezel ?? 20) / 1000);
	let opacity = $derived(xray ? 0.2 : good ? 1 : 0.5);
	let shell = $derived({ color: '#6f7377', metalness: 0.6, roughness: 0.45, transparent: opacity < 1, opacity, depthWrite: opacity >= 1 });
	let hit = $derived(xray ? noRaycast : Mesh.prototype.raycast);
	let board = $derived(profile.boardPlate ? { pos: mm(profile.boardPlate.pos), size: mm(profile.boardPlate.size) } : undefined);
	let backplane = $derived(profile.backplane ? { pos: mm(profile.backplane.pos), size: mm(profile.backplane.size) } : undefined);

	// The label, as the AR layer will see it: a square on the anchor's plane,
	// its three finder patterns at the corners away from the bottom right.
	let qr = $derived(profile.anchors?.qr);
	let qrQuat = $derived.by(() => {
		if (!qr) return [0, 0, 0] as Vec3;
		// Plane lies in xy facing +z; turn it to face `normal` with `up` up.
		const [nx, ny, nz] = qr.normal;
		if (ny > 0.9) return [-Math.PI / 2, 0, Math.atan2(-qr.up[0], -qr.up[2])] as Vec3;
		if (ny < -0.9) return [Math.PI / 2, 0, 0] as Vec3;
		return [0, Math.atan2(nx, nz), 0] as Vec3;
	});
</script>

<!-- floor -->
<T.Mesh position={[0, floorT / 2, -D / 2]} raycast={hit}>
	<T.BoxGeometry args={[W, floorT, D]} />
	<T.MeshStandardMaterial {...shell} />
</T.Mesh>
<!-- side walls -->
{#each [-1, 1] as side}
	<T.Mesh position={[side * (W / 2 - wall / 2), H / 2, -D / 2]} raycast={noRaycast}>
		<T.BoxGeometry args={[wall, H, D]} />
		<T.MeshStandardMaterial {...shell} />
	</T.Mesh>
	<!-- front ear: the rack flange with the control panel -->
	<T.Mesh position={[side * (W / 2 - ear / 2), H / 2, -0.002]} raycast={noRaycast}>
		<T.BoxGeometry args={[ear, H, 0.004]} />
		<T.MeshStandardMaterial color="#2a2b2d" metalness={0.4} roughness={0.5} transparent={opacity < 1} {opacity} />
	</T.Mesh>
{/each}
{#if board}
	<T.Mesh position={board.pos} raycast={noRaycast}>
		<T.BoxGeometry args={board.size} />
		<T.MeshStandardMaterial color="#123a24" roughness={0.6} />
	</T.Mesh>
{/if}
{#if backplane}
	<T.Mesh position={backplane.pos} raycast={noRaycast}>
		<T.BoxGeometry args={backplane.size} />
		<T.MeshStandardMaterial color="#1d4a2e" roughness={0.6} />
	</T.Mesh>
{/if}
{#if lid === 'on' || xray}
	<T.Group position={lidOffset}>
		<T.Mesh position={[0, H - lidT / 2, -D / 2]} raycast={hit}>
			<T.BoxGeometry args={[W, lidT, D]} />
			<T.MeshStandardMaterial {...shell} />
		</T.Mesh>
		{#if anchor && qr}
			{@const s = qr.size / 1000}
			<T.Group position={[qr.pos[0] / 1000, qr.pos[1] / 1000 + 0.0006, qr.pos[2] / 1000]} rotation={qrQuat}>
				<T.Mesh raycast={noRaycast}>
					<T.PlaneGeometry args={[s, s]} />
					<T.MeshBasicMaterial color="#f4f4f0" transparent={xray} opacity={xray ? 0.5 : 1} />
				</T.Mesh>
				{#each [[-1, 1], [1, 1], [-1, -1]] as [fx, fy]}
					<T.Mesh position={[(fx * s * 0.34), (fy * s * 0.34), 0.0003]} raycast={noRaycast}>
						<T.PlaneGeometry args={[s * 0.24, s * 0.24]} />
						<T.MeshBasicMaterial color="#111" transparent={xray} opacity={xray ? 0.5 : 1} />
					</T.Mesh>
				{/each}
			</T.Group>
		{/if}
	</T.Group>
{/if}
