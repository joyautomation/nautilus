<script lang="ts">
	// The simulation's clock, when the stream comes from the replay server
	// (randd/node-3d-sim/sim/replay.mjs): each frame carries `replay` — the
	// recorded moment and the speed. Speed buttons and a scrubber across the
	// whole recording set it through /api/replay. Hidden on a live
	// controller, whose frames carry no `replay`.
	interface Replay {
		t: number;
		iso: string;
		speed: number;
		t0: number;
		t1: number;
	}
	let { replay }: { replay?: Replay } = $props();
	const SPEEDS = [0, 1, 60, 600];
	let dragging = $state<number | null>(null);
	let at = $derived(replay ? (dragging ?? (replay.t - replay.t0) / (replay.t1 - replay.t0)) : 0);
	const when = (t: number) =>
		new Date(t * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'America/Los_Angeles' });
	const send = (q: string) => fetch(`/api/replay?${q}`).catch(() => {});
</script>

{#if replay}
	<div class="clock" role="group" aria-label="Replay">
		<span class="dot" class:paused={replay.speed === 0}></span>
		<b>{when(dragging !== null ? replay.t0 + dragging * (replay.t1 - replay.t0) : replay.t)}</b>
		<span class="muted">recorded</span>
		{#each SPEEDS as s}
			<button class:on={replay.speed === s} onclick={() => send(`speed=${s}`)}>{s === 0 ? '❚❚' : `×${s}`}</button>
		{/each}
		<input
			type="range"
			min="0"
			max="1"
			step="0.0005"
			value={at}
			aria-label="Replay position"
			oninput={(e) => (dragging = +e.currentTarget.value)}
			onchange={(e) => {
				send(`at=${e.currentTarget.value}`);
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
		max-width: min(520px, calc(100vw - 24px));
		padding: 8px 10px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 90%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.2 system-ui, sans-serif;
		z-index: 5;
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
		.clock {
			left: 12px;
			right: 12px;
			bottom: auto;
			top: 96px;
		}
	}
</style>
