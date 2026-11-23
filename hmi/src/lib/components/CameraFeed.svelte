<script lang="ts">
	// One camera (#724), used by CameraPanel. A refreshing still by default;
	// live video only after the person clicks "Watch live", and it stops again
	// on Stop, when the tab is hidden, when the stream fails and when this goes
	// away. The still pauses while the tab is hidden and refreshes the moment
	// it is visible again. A failure shows the controller's own words.
	import { onMount } from 'svelte';
	import { playHls } from '../hls.js';
	import { CameraClient, formatBps, LIVE_LIMIT, nextState, watchingText, isLive, INITIAL, LIVE_CAPTION, type Camera, type CameraEvent, type CameraState, NO_VIEW } from '../camera.js';

	let { camera, client }: { camera: Camera; client: CameraClient } = $props();

	let st = $state<CameraState>(INITIAL);
	let nonce = $state(Date.now());
	let seen = $state<Camera | null>(null);
	// the latest list reading while live (bitrate, watchers); the prop until then
	const now = $derived(seen && seen.id === camera.id ? seen : camera);
	const live = $derived(isLive(st));
	const src = $derived(live ? client.streamUrl(camera.id) : client.snapshotUrl(camera.id, nonce));
	const hls = $derived(camera.hls);
	let video = $state<HTMLVideoElement | null>(null);
	// HLS: the bitrate the player measured (null: none yet), and whether the browser plays it itself
	let measured = $state<number | null>(null);
	let native = $state(false);
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

	// HLS live: started only here, which runs only while live (a click) and ends on
	// Stop, a hidden tab, destroy and any error
	$effect(() => {
		if (!live || !hls || !video) return;
		measured = null;
		const h = playHls(video, client.hlsUrl(camera.id), {
			onBitrate: (bps) => (measured = bps),
			onError: (message, status) => send({ type: 'streamError', status, message: status === 410 ? LIVE_LIMIT : message })
		});
		native = h.native;
		return () => h.stop();
	});

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
	{#if !camera.view}
		<p class="msg bad" role="status">{NO_VIEW}</p>
	{:else}
	{#if live && hls}
		<!-- svelte-ignore a11y_media_has_caption -->
		<video bind:this={video} muted playsinline autoplay aria-label={alt}></video>
	{:else}
	{#key live}
		<img {src} {alt} onerror={failed} onload={loaded} />
	{/key}
	{/if}
	<div class="bar">
		{#if live}
			<button type="button" class="btn" onclick={() => send({ type: 'stop' })}>Stop</button>
			<span class="stat">{watchingText(now)} · {#if !hls}{formatBps(now.bps)}{:else if native}bitrate not measured{:else}{measured === null ? '…' : formatBps(measured)}{/if}</span>
		{:else if camera.live}
			<button type="button" class="btn" onclick={() => send({ type: 'watch' })}>Watch live</button>
		{/if}
	</div>
	{#if live}<p class="caption">{LIVE_CAPTION}</p>{/if}
	<p class="msg" class:bad={st.message !== ''} role="status" aria-live="polite">
		{#if st.message}{st.mode === 'busy' ? 'Live video is not available right now: ' : 'Camera: '}{st.message}{/if}
	</p>
	{/if}
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
	img,
	video {
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
