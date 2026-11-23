<script lang="ts">
	// One camera (#724), used by CameraPanel. A refreshing still by default;
	// live video only after the person clicks "Watch live", and it stops again
	// on Stop, when the tab is hidden, when the stream fails and when this goes
	// away. The still pauses while the tab is hidden and refreshes the moment
	// it is visible again. A failure shows the controller's own words.
	import { onMount } from 'svelte';
	import { CameraClient, formatBps, nextState, watchingText, isLive, INITIAL, LIVE_CAPTION, type Camera, type CameraEvent, type CameraState } from '../camera.js';

	let { camera, client }: { camera: Camera; client: CameraClient } = $props();

	let st = $state<CameraState>(INITIAL);
	let nonce = $state(Date.now());
	let seen = $state<Camera | null>(null);
	// the latest list reading while live (bitrate, watchers); the prop until then
	const now = $derived(seen && seen.id === camera.id ? seen : camera);
	const live = $derived(isLive(st));
	const src = $derived(live ? client.streamUrl(camera.id) : client.snapshotUrl(camera.id, nonce));
	const alt = $derived(`${camera.label} camera`);

	function send(e: CameraEvent) {
		st = nextState(st, e, camera.live);
	}

	async function failed() {
		// an <img> cannot show a JSON body: ask the controller what it said
		const url = src;
		const was = live;
		const r = await client.describe(url);
		if (url !== src) return; // a newer image is already showing
		if (was) send({ type: 'streamError', status: r.status, message: r.message });
		else send({ type: 'snapshotError', message: r.message });
	}

	function loaded() {
		if (!live) send({ type: 'snapshotOk' });
	}

	onMount(() => {
		const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';
		const onVis = () => {
			if (visible()) nonce = Date.now();
			else send({ type: 'hidden' });
		};
		document.addEventListener('visibilitychange', onVis);
		return () => {
			document.removeEventListener('visibilitychange', onVis);
			send({ type: 'destroyed' });
		};
	});

	// snapshots at the camera's interval (also retrying after a failure), paused while hidden
	$effect(() => {
		if (live) return;
		const id = setInterval(() => {
			if (document.visibilityState !== 'hidden') nonce = Date.now();
		}, Math.max(1, camera.interval) * 1000);
		return () => clearInterval(id);
	});

	// the measured bitrate: read the list every 3 s while live
	$effect(() => {
		if (!live) {
			seen = null;
			return;
		}
		let stop = false;
		const read = async () => {
			const l = await client.list();
			const c = l.find((x) => x.id === camera.id);
			if (!stop && c) seen = c;
		};
		void read();
		const id = setInterval(read, 3000);
		return () => {
			stop = true;
			clearInterval(id);
		};
	});
</script>

<figure class="feed">
	<figcaption>
		<span class="name">{camera.label}</span>
		<span class="mode">{live ? 'Live video' : 'Still image, refreshes every ' + camera.interval + ' s'}</span>
	</figcaption>
	{#key live}
		<img {src} {alt} onerror={failed} onload={loaded} />
	{/key}
	<div class="bar">
		{#if live}
			<button type="button" class="btn" onclick={() => send({ type: 'stop' })}>Stop</button>
			<span class="stat">{watchingText(now)} · {formatBps(now.bps)}</span>
		{:else if camera.live}
			<button type="button" class="btn" onclick={() => send({ type: 'watch' })}>Watch live</button>
		{/if}
	</div>
	{#if live}<p class="caption">{LIVE_CAPTION}</p>{/if}
	<p class="msg" class:bad={st.message !== ''} role="status" aria-live="polite">
		{#if st.message}{st.mode === 'busy' ? 'Live video is not available right now: ' : 'Camera: '}{st.message}{/if}
	</p>
</figure>

<style>
	.feed {
		margin: 0;
		display: grid;
		gap: var(--space-2, 8px);
		min-width: 0;
	}
	figcaption {
		display: flex;
		gap: var(--space-2, 8px);
		align-items: baseline;
		flex-wrap: wrap;
	}
	.name {
		font-weight: 600;
	}
	.mode,
	.stat,
	.caption {
		color: var(--muted);
		font-size: var(--font-xs, 0.85rem);
	}
	img {
		max-width: 100%;
		border-radius: var(--radius-sm);
		background: var(--surface-2);
	}
	.bar {
		display: flex;
		gap: var(--space-2, 8px);
		align-items: center;
		flex-wrap: wrap;
	}
	.caption {
		margin: 0;
	}
	.msg {
		margin: 0;
		font-size: var(--font-xs, 0.85rem);
	}
	.msg.bad {
		color: var(--ink);
		border-left: 3px solid var(--warn, #d98c00);
		padding-left: var(--space-2, 8px);
	}
</style>
