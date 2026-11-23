<script lang="ts">
	// The controller's cameras (#724) that look at one piece of equipment. It
	// reads the list once and draws nothing at all (no heading, no empty box)
	// when this controller has none for the asset. Each camera is a
	// CameraFeed: a still by default, live video only on a click.
	import { onMount } from 'svelte';
	import { CameraClient, camerasFor, type Camera } from '../camera.js';
	import CameraFeed from './CameraFeed.svelte';

	let { asset, client = new CameraClient() }: { asset: string; client?: CameraClient } = $props();

	let all = $state<Camera[]>([]);
	onMount(() => {
		let gone = false;
		void client.list().then((l) => {
			if (!gone) all = l;
		});
		return () => {
			gone = true;
		};
	});
	const mine = $derived(camerasFor(all, asset));
</script>

{#if mine.length}
	<div class="cams">
		{#each mine as c (c.id)}
			<CameraFeed camera={c} {client} />
		{/each}
	</div>
{/if}

<style>
	.cams {
		display: grid;
		gap: var(--space-3, 12px);
	}
</style>
