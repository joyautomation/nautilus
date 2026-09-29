<script lang="ts">
	// The chassis a profile describes: floor, side walls, front ears, the
	// board plate and the drive backplane, a removable lid behind a fixed front cover, fixed internals, and the
	// printed codes where the profile's anchors put them. Metres, in the profile's
	// frame (origin at the bottom centre of the front face, +z out of the
	// front). Only the floor and an opaque lid take clicks — a wall seen
	// edge-on must not steal a pick from the part behind it, and in x-ray
	// nothing of the shell does.
	import { T } from '@threlte/core';
	import { Euler, Matrix4, Mesh, NearestFilter, SRGBColorSpace, TextureLoader, Vector3, type Texture } from 'three';
	import { mm, type ChassisProfile } from './profile.js';
	import type { Vec3 } from '../scene.js';

	let {
		profile,
		lid = 'on',
		xray = false,
		lidOffset = [0, 0, 0],
		anchor = true,
		codeImages = {},
		good = true
	}: {
		profile: ChassisProfile;
		lid?: 'on' | 'off';
		xray?: boolean;
		/** The lid's exploded offset, metres. */
		lidOffset?: Vec3;
		/** Draw the printed codes at the profile's anchors. */
		anchor?: boolean;
		/** Anchor id → an image of its printed code (a data URL). Without
		 * one, the code is drawn as a placeholder: white, three finders. */
		codeImages?: Record<string, string>;
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
	let shell = $derived({ color: profile.shell?.color ?? '#6f7377', metalness: 0.6, roughness: 0.45, transparent: opacity < 1, opacity, depthWrite: opacity >= 1 });
	let hit = $derived(xray ? noRaycast : Mesh.prototype.raycast);
	let board = $derived(profile.boardPlate ? { pos: mm(profile.boardPlate.pos), size: mm(profile.boardPlate.size) } : undefined);
	let cover = $derived((profile.shell?.frontCover ?? 0) / 1000);
	let fixtures = $derived((profile.fixtures ?? []).map((f) => ({ pos: mm(f.pos), size: mm(f.size) })));
	let backplane = $derived(profile.backplane ? { pos: mm(profile.backplane.pos), size: mm(profile.backplane.size) } : undefined);

	// The real codes, when the app supplies them: crisp modules, no smoothing.
	const loader = new TextureLoader();
	let textures = $derived.by(() => {
		const out: Record<string, Texture> = {};
		for (const [id, url] of Object.entries(codeImages)) {
			const t = loader.load(url);
			t.magFilter = NearestFilter;
			t.minFilter = NearestFilter;
			t.generateMipmaps = false;
			t.colorSpace = SRGBColorSpace;
			out[id] = t;
		}
		return out;
	});
	$effect(() => {
		const t = textures;
		return () => Object.values(t).forEach((x) => x.dispose());
	});

	// The printed codes, as the AR layer will see them: a square on each
	// anchor's plane, turned so its top edge points along `up`, its three
	// finder patterns at the corners away from the bottom right.
	let codes = $derived(
		Object.entries(profile.anchors ?? {}).map(([id, a]) => {
			const n = new Vector3(...a.normal).normalize();
			const up = new Vector3(...a.up).normalize();
			const right = new Vector3().crossVectors(up, n);
			const e = new Euler().setFromRotationMatrix(new Matrix4().makeBasis(right, up, n));
			// Lift it off its surface along the normal, so it never z-fights.
			const p = mm(a.pos).map((v, i) => v + [n.x, n.y, n.z][i] * 0.0006) as Vec3;
			return { id, pos: p, rot: [e.x, e.y, e.z] as Vec3, s: a.size / 1000 };
		})
	);
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
{#each fixtures as f}
	<T.Mesh position={f.pos} raycast={noRaycast}>
		<T.BoxGeometry args={f.size} />
		<T.MeshStandardMaterial color="#8a8d91" metalness={0.6} roughness={0.4} transparent={opacity < 1} {opacity} />
	</T.Mesh>
{/each}
<!-- the fixed cover over the drive cage: stays when the lid comes off -->
{#if cover > 0}
	<T.Mesh position={[0, H - lidT / 2, -cover / 2]} raycast={hit}>
		<T.BoxGeometry args={[W, lidT, cover]} />
		<T.MeshStandardMaterial {...shell} />
	</T.Mesh>
{/if}
<!-- the printed codes, where the profile's anchors put them -->
{#if anchor}
	{#each codes as c (c.id)}
		<T.Group position={c.pos} rotation={c.rot}>
			{#if textures[c.id]}
				<T.Mesh raycast={noRaycast}>
					<T.PlaneGeometry args={[c.s, c.s]} />
					<T.MeshBasicMaterial map={textures[c.id]} transparent={xray} opacity={xray ? 0.5 : 1} />
				</T.Mesh>
			{:else}
				<T.Mesh raycast={noRaycast}>
					<T.PlaneGeometry args={[c.s, c.s]} />
					<T.MeshBasicMaterial color="#f4f4f0" transparent={xray} opacity={xray ? 0.5 : 1} />
				</T.Mesh>
				{#each [[-1, 1], [1, 1], [-1, -1]] as [fx, fy]}
					<T.Mesh position={[fx * c.s * 0.34, fy * c.s * 0.34, 0.0003]} raycast={noRaycast}>
						<T.PlaneGeometry args={[c.s * 0.24, c.s * 0.24]} />
						<T.MeshBasicMaterial color="#111" transparent={xray} opacity={xray ? 0.5 : 1} />
					</T.Mesh>
				{/each}
			{/if}
		</T.Group>
	{/each}
{/if}
{#if lid === 'on' || xray}
	<T.Group position={lidOffset}>
		<T.Mesh position={[0, H - lidT / 2, -cover - (D - cover) / 2]} raycast={hit}>
			<T.BoxGeometry args={[W, lidT, D - cover]} />
			<T.MeshStandardMaterial {...shell} />
		</T.Mesh>
	</T.Group>
{/if}
