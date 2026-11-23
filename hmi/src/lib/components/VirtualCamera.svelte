<script lang="ts">
	// The playground's camera (#724): a test pattern drawn on a canvas, because the
	// browser build has no camera relay and no network to reach one. It asks for
	// nothing, shows only while the tab is visible, and says it is simulated.
	import { onMount } from 'svelte';
	import { drawTestPattern } from '../testpattern.js';
	import type { Camera } from '../camera.js';

	let { camera }: { camera: Camera } = $props();

	let canvas = $state<HTMLCanvasElement | null>(null);
	const alt = $derived(`${camera.label} (simulated camera)`);

	onMount(() => {
		if (!canvas) return;
		const ctx = canvas.getContext('2d');
		if (!ctx) return;
		let raf = 0;
		let last = 0;
		const t0 = performance.now();
		const frame = (now: number) => {
			raf = requestAnimationFrame(frame);
			if (now - last < 100) return; // ten frames a second is plenty for a pattern
			last = now;
			drawTestPattern(ctx as unknown as Parameters<typeof drawTestPattern>[0], canvas!.width, canvas!.height, (now - t0) / 1000, camera.label);
		};
		// a person who asked for less motion gets one still frame, not a sweeping line
		const still = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
		const run = () => {
			cancelAnimationFrame(raf);
			if (still) {
				drawTestPattern(ctx as unknown as Parameters<typeof drawTestPattern>[0], canvas!.width, canvas!.height, 0, camera.label);
				return;
			}
			if (typeof document === 'undefined' || document.visibilityState !== 'hidden') raf = requestAnimationFrame(frame);
		};
		run();
		document.addEventListener('visibilitychange', run);
		return () => {
			cancelAnimationFrame(raf);
			document.removeEventListener('visibilitychange', run);
		};
	});
</script>

<figure class="feed">
	<figcaption>
		<span class="name">{camera.label}</span>
		<span class="mode">Simulated camera, drawn in your browser</span>
	</figcaption>
	<canvas bind:this={canvas} width="640" height="360" aria-label={alt}>{alt}</canvas>
</figure>

<style>
	.feed {
		margin: 0;
		display: grid;
		gap: var(--space-2, 8px);
	}
	figcaption {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2, 8px);
		align-items: baseline;
	}
	.name {
		font-weight: 600;
	}
	.mode {
		color: var(--color-muted, #667);
		font-size: 0.875em;
	}
	canvas {
		width: 100%;
		height: auto;
		aspect-ratio: 16 / 9;
		background: #101418;
		border-radius: var(--radius-2, 6px);
	}
</style>
