<script lang="ts">
	// What is in alarm, by name: each alarm with its priority, its state and
	// how long ago; a tap takes the 3D to it (`onshow`). Acknowledge one or
	// all. The signs' key, folded, under the list. On a phone it is a chip
	// with the count, coloured by the worst still active (Sheet).
	import type { AlarmClient } from '@joyautomation/nautilus-hmi';
	import { AlarmKey, type Palette } from '@joyautomation/nautilus-hmi-3d';
	import Sheet from './Sheet.svelte';
	import { sheets } from './sheets.svelte';
	import { ago, listAlarms, stateText, worst, type Alarm } from './alarmlist';

	let { alarms, palette, onshow }: { alarms: AlarmClient; palette: Palette; onshow?: (a: Alarm) => void } = $props();
	let list = $derived(listAlarms(alarms.instances as Alarm[]));
	let top = $derived(worst(list));
	let live = $derived(list.filter((a) => a.state.endsWith('-active')).length);
	let unacked = $derived(list.filter((a) => a.state.startsWith('unack')).length);
	let now = $state(Date.now());
	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 5000);
		return () => clearInterval(id);
	});
	// The same glyphs the 3D signs use, by priority (AlarmKey).
	const GLYPH: Record<string, string> = { critical: '⬣', high: '▲', medium: '◆', low: '●', diagnostic: '·' };

	function show(a: Alarm) {
		onshow?.(a);
		if (sheets.phone) sheets.open = null;
	}
</script>

{#if list.length}
	<Sheet id="alarms" label="Alarms" fixed>
		{#snippet chip()}<i class="g {top}">{GLYPH[top ?? 'low']}</i>{live ? `${live} in alarm` : `${list.length} to acknowledge`}{/snippet}
		<div class="box">
			<header>
				<b>{live} in alarm{#if list.length > live}<span class="muted"> · {list.length - live} cleared</span>{/if}</b>
				{#if unacked}<button class="ack" onclick={() => alarms.ack('all', 'node-3d')}>ack all</button>{/if}
			</header>
			<ul>
				{#each list as a (a.id)}
					<li class:cleared={!a.state.endsWith('-active')} class:new={a.state === 'unack-active'}>
						<button class="go" onclick={() => show(a)} title="Show it in the 3D">
							<i class="g {a.priority}">{GLYPH[a.priority]}</i>
							<span class="what">
								<b>{a.name}</b>
								<small>{a.priority} · {stateText(a.state)}{#if a.activeMs} · {ago(a.activeMs, now)}{/if}</small>
							</span>
						</button>
						{#if a.state.startsWith('unack')}
							<button class="ack" onclick={() => alarms.ack([a.id], 'node-3d')}>ack</button>
						{/if}
					</li>
				{/each}
			</ul>
			<details>
				<summary>what the signs mean</summary>
				<AlarmKey {palette} />
			</details>
		</div>
	</Sheet>
{/if}

<style>
	.box {
		display: grid;
		gap: 6px;
		width: min(320px, calc(100vw - 48px));
	}
	@media (max-width: 600px) {
		.box {
			width: auto;
		}
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}
	.muted {
		color: var(--ink-2, #a8a6a1);
		font-weight: 400;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 2px;
		max-height: 40vh;
		overflow-y: auto;
	}
	li {
		display: flex;
		align-items: center;
		gap: 4px;
		border-radius: 6px;
	}
	li.cleared {
		opacity: 0.7;
	}
	li.new {
		background: color-mix(in srgb, var(--crit, #e5484d) 10%, transparent);
	}
	.go {
		flex: 1;
		display: flex;
		align-items: flex-start;
		gap: 8px;
		min-width: 0;
		padding: 5px 6px;
		border: 0;
		border-radius: 6px;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}
	.go:hover {
		background: color-mix(in srgb, var(--axis, #383835) 60%, transparent);
	}
	.what {
		display: grid;
		min-width: 0;
	}
	small {
		color: var(--ink-2, #a8a6a1);
	}
	.g {
		font-style: normal;
		width: 14px;
		text-align: center;
	}
	.critical {
		color: var(--crit, #e5484d);
	}
	.high {
		color: var(--serious, #f08c5a);
	}
	.medium {
		color: var(--warn, #d9a441);
	}
	.low,
	.diagnostic {
		color: var(--ink-2, #a8a6a1);
	}
	.ack {
		font: 11px/1 system-ui, sans-serif;
		padding: 4px 8px;
		border-radius: 999px;
		border: 1px solid var(--axis, #383835);
		background: transparent;
		color: var(--ink, #e8e6e1);
		cursor: pointer;
	}
	summary {
		cursor: pointer;
		color: var(--ink-2, #a8a6a1);
	}
</style>
