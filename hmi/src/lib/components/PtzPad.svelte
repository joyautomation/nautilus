<script lang="ts">
	// Pan, tilt and zoom for one camera (#724), under its live video. A held
	// button moves the camera and letting go (or leaving the button, hiding the tab,
	// closing the page) stops it; the controller also stops it by itself after
	// ten seconds. Shown only to a person who holds the `ptz` permission: moving a
	// camera asks for the password once (the kit does it) and then every move
	// inside the elevation window goes straight through.
	import { onMount } from 'svelte';
	import { CameraClient, ptzMove, PTZ_HOLD_MS, PTZ_NUDGE_MS, type PtzDirection, type PtzPreset } from '../camera.js';

	let { id, client }: { id: string; client: CameraClient } = $props();

	let presets = $state<PtzPreset[]>([]);
	let preset = $state('');
	let message = $state('');
	let held: PtzDirection | null = null;

	async function send(p: Promise<{ ok: boolean; status: number; message: string }>) {
		const r = await p;
		// too many moves at once is a finger on a button, not a failure to report
		message = r.ok || r.status === 429 ? '' : r.message;
	}

	function press(dir: PtzDirection) {
		held = dir;
		void send(client.ptz(id, ptzMove(dir, PTZ_HOLD_MS)));
	}

	function release() {
		if (!held) return;
		held = null;
		void send(client.ptz(id, { action: 'stop' }));
	}

	function nudge(e: KeyboardEvent, dir: PtzDirection) {
		if (e.key !== 'Enter' && e.key !== ' ') return;
		e.preventDefault();
		if (e.repeat) return;
		void send(client.ptz(id, ptzMove(dir, PTZ_NUDGE_MS)));
	}

	onMount(() => {
		void client.ptzPresets(id).then((p) => (presets = p));
		const hidden = () => document.visibilityState === 'hidden' && release();
		document.addEventListener('visibilitychange', hidden);
		window.addEventListener('blur', release);
		window.addEventListener('pointerup', release);
		return () => {
			document.removeEventListener('visibilitychange', hidden);
			window.removeEventListener('blur', release);
			window.removeEventListener('pointerup', release);
			release();
		};
	});

	const pad: Array<{ dir: PtzDirection; label: string; glyph: string }> = [
		{ dir: 'up', label: 'Tilt up', glyph: '↑' },
		{ dir: 'left', label: 'Pan left', glyph: '←' },
		{ dir: 'right', label: 'Pan right', glyph: '→' },
		{ dir: 'down', label: 'Tilt down', glyph: '↓' },
		{ dir: 'in', label: 'Zoom in', glyph: '+' },
		{ dir: 'out', label: 'Zoom out', glyph: '−' }
	];
</script>

<div class="ptz" role="group" aria-label="Move the camera">
	<div class="pad">
		{#each pad as b (b.dir)}
			<button
				type="button"
				class={b.dir}
				aria-label={b.label}
				title="{b.label}: hold to move"
				onpointerdown={() => press(b.dir)}
				onpointerleave={release}
				onpointercancel={release}
				onkeydown={(e) => nudge(e, b.dir)}
			>
				{b.glyph}
			</button>
		{/each}
		<button type="button" class="stop" aria-label="Stop the camera" onclick={() => send(client.ptz(id, { action: 'stop' }))}>■</button>
	</div>
	{#if presets.length}
		<label class="presets">
			<span>Go to</span>
			<select bind:value={preset}>
				<option value="">a preset…</option>
				{#each presets as p (p.token)}
					<option value={p.token}>{p.name}</option>
				{/each}
			</select>
		</label>
		<button type="button" disabled={!preset} onclick={() => send(client.ptz(id, { action: 'preset', preset }))}>Go</button>
	{/if}
	{#if message}<p class="msg" role="status">{message}</p>{/if}
</div>

<style>
	.ptz {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2, 8px);
	}
	.pad {
		display: grid;
		grid-template-columns: repeat(3, 2.5rem);
		grid-template-areas: '. up in' 'left stop right' 'out down .';
		gap: 4px;
	}
	.pad button {
		min-height: 2.5rem;
		touch-action: none;
		user-select: none;
	}
	.up { grid-area: up; }
	.down { grid-area: down; }
	.left { grid-area: left; }
	.right { grid-area: right; }
	.in { grid-area: in; }
	.out { grid-area: out; }
	.stop { grid-area: stop; }
	.presets {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	.msg {
		flex-basis: 100%;
		margin: 0;
		color: var(--color-danger, #b00020);
	}
</style>
