<script lang="ts">
	// The simulation's clock. A simulated cluster is two controllers: the
	// monitoring one this page reads (/api), and the plant that plays the
	// recording and takes the faults (/plant/api, served when PLANT_URL is
	// set; examples/it-cluster in nautilus). The plant's replay driver owns
	// the clock as tags: Replay_At/From/To report it, Replay_Speed/Pause/
	// SeekS steer it. Hidden when there is no plant: a live controller.
	interface Clock {
		at: number;
		from: number;
		to: number;
		speed: number;
		paused: boolean;
		seek: number;
	}
	const SPEEDS = [1, 60, 600];
	let clock = $state<Clock | null>(null);
	let dragging = $state<number | null>(null);
	let pos = $derived(clock && clock.to > clock.from ? (dragging ?? (clock.at - clock.from) / (clock.to - clock.from)) : 0);
	const when = (t: number) =>
		new Date(t * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'America/Los_Angeles' });

	async function poll() {
		try {
			const r = await fetch('/plant/api/state?tags=Replay_*');
			if (!r.ok) throw new Error(r.statusText);
			const t = (await r.json()).tags ?? {};
			clock = t.Replay_At
				? { at: t.Replay_At, from: t.Replay_From, to: t.Replay_To, speed: t.Replay_Speed, paused: t.Replay_Pause, seek: t.Replay_SeekS }
				: null;
		} catch {
			clock = null;
		}
	}
	$effect(() => {
		poll();
		const id = setInterval(poll, 1000);
		return () => clearInterval(id);
	});

	async function write(name: string, value: number | boolean) {
		await fetch('/plant/api/tags', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ name, value })
		}).catch(() => {});
		poll();
	}
	const setSpeed = async (s: number) => {
		await write('Replay_Speed', s);
		await write('Replay_Pause', false);
	};
	function seek(frac: number) {
		if (!clock) return;
		let t = Math.round(clock.from + frac * (clock.to - clock.from));
		if (t === clock.seek) t += 1; // the driver jumps on a CHANGE of SeekS
		write('Replay_SeekS', t);
	}
</script>

{#if clock}
	<div class="clock" role="group" aria-label="Replay">
		<span class="sim">SIMULATION</span>
		<span class="dot" class:paused={clock.paused}></span>
		<b>{when(dragging !== null ? clock.from + dragging * (clock.to - clock.from) : clock.at)}</b>
		<span class="muted">recorded</span>
		<button class:on={clock.paused} onclick={() => write('Replay_Pause', !clock!.paused)} aria-label="Pause">❚❚</button>
		{#each SPEEDS as s}
			<button class:on={!clock.paused && clock.speed === s} onclick={() => setSpeed(s)}>×{s}</button>
		{/each}
		<input
			type="range"
			min="0"
			max="1"
			step="0.0005"
			value={pos}
			aria-label="Replay position"
			oninput={(e) => (dragging = +e.currentTarget.value)}
			onchange={(e) => {
				seek(+e.currentTarget.value);
				dragging = null;
			}}
		/>
	</div>
{/if}

<style>
	.clock {
		position: fixed;
		right: 12px;
		bottom: 12px;
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		max-width: min(560px, calc(100vw - 24px));
		padding: 8px 10px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.2 system-ui, sans-serif;
		z-index: 5;
	}
	.sim {
		font: 600 10px/1 system-ui, sans-serif;
		letter-spacing: 0.08em;
		padding: 3px 6px;
		border-radius: 4px;
		border: 1px solid var(--warn, #d9a441);
		color: var(--warn, #d9a441);
	}
	b {
		font-variant-numeric: tabular-nums;
	}
	.muted {
		color: var(--ink-2, #a8a6a1);
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--accent, #6aa5e8);
	}
	.dot.paused {
		background: var(--ink-2, #a8a6a1);
	}
	button {
		font: 11px/1 system-ui, sans-serif;
		padding: 4px 8px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: transparent;
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
	button.on {
		border-color: var(--accent, #6aa5e8);
		color: var(--accent, #6aa5e8);
	}
	input {
		flex: 1 1 160px;
		accent-color: var(--accent, #6aa5e8);
	}
	@media (max-width: 600px) {
		/* along the bottom, under the chip row (sheets.svelte.ts) */
		.clock {
			left: 12px;
			right: 12px;
		}
		.muted {
			display: none;
		}
	}
</style>
