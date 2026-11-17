<script lang="ts" module>
	import { CanvasTexture } from 'three';

	// One soft disc, shared by every lit LED's glow.
	let glowMap: CanvasTexture | undefined;
	function glow(): CanvasTexture {
		if (glowMap) return glowMap;
		const c = document.createElement('canvas');
		c.width = c.height = 64;
		const ctx = c.getContext('2d')!;
		const g = ctx.createRadialGradient(32, 32, 0, 32, 32, 32);
		g.addColorStop(0, 'rgba(255,255,255,0.9)');
		g.addColorStop(0.25, 'rgba(255,255,255,0.35)');
		g.addColorStop(1, 'rgba(255,255,255,0)');
		ctx.fillStyle = g;
		ctx.fillRect(0, 0, 64, 64);
		glowMap = new CanvasTexture(c);
		return glowMap;
	}
</script>

<script lang="ts">
	// An indicator on a module's face: a small lens, dark when off, and
	// when on its own colour unlit by the scene plus a soft glow, so a lit
	// channel reads from across the room.
	import { T } from '@threlte/core';
	import { AdditiveBlending } from 'three';

	let {
		position,
		size = [3, 1.6],
		on = false,
		color = '#52ff7d',
		off = '#2b3037',
		spread = 3.2
	}: {
		position: [number, number, number];
		/** Width and height of the lens, mm. */
		size?: [number, number];
		on?: boolean;
		color?: string;
		off?: string;
		/** The glow's size as a multiple of the lens's longer side. */
		spread?: number;
	} = $props();
</script>

<T.Group {position}>
	<T.Mesh>
		<T.BoxGeometry args={[size[0], size[1], 0.5]} />
		<!-- one material for both states: swapping materials under a mesh leaves it with three's default white one -->
		<T.MeshBasicMaterial color={on ? color : off} toneMapped={false} />
	</T.Mesh>
	{#if on}
		<T.Mesh position={[0, 0, 0.4]} renderOrder={3}>
			<T.PlaneGeometry args={[Math.max(...size) * spread, Math.max(...size) * spread]} />
			<T.MeshBasicMaterial map={glow()} {color} transparent opacity={0.85} blending={AdditiveBlending} depthWrite={false} toneMapped={false} />
		</T.Mesh>
	{/if}
</T.Group>
