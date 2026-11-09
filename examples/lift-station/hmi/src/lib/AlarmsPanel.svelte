<script lang="ts">
	// The station's alarms, from the alarm banner in the bar: the active
	// ones (the kit's AlarmTable: acknowledge, shelve and unshelve, filter,
	// sort) and the history (AlarmJournal: every raise, clear, ack and
	// shelve in a time range, from the controller's journal). It slides in
	// on the right over whichever view is showing; × or Escape closes it.
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { AlarmTable, AlarmJournal, type AlarmEvent } from '@joyautomation/nautilus-hmi';
	import { alarms } from '$lib/client.svelte';

	let { open = $bindable(false) }: { open?: boolean } = $props();

	let tab = $state<'active' | 'history'>('active');
	let now = $state(Date.now());
	let events = $state<AlarmEvent[]>([]);
	let loading = $state(false);
	let range = $state({ from: Date.now() - 24 * 3600_000, to: Date.now() });

	async function load(from: number, to: number) {
		range = { from, to };
		loading = true;
		events = (await alarms.journal(from, to)).events;
		loading = false;
	}

	// The table's ages count up while it is open; the history loads when
	// first shown, for the last day.
	$effect(() => {
		if (!open) return;
		const id = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(id);
	});
	$effect(() => {
		if (open && tab === 'history') void load(Date.now() - 24 * 3600_000, Date.now());
	});

	const still = typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches;
	const slide = { x: 760, opacity: 1, duration: still ? 0 : 260, easing: cubicOut };
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (open = false)} />

{#if open}
	<aside class="panel" aria-label="Alarms" transition:fly={slide}>
		<header>
			<h2>Alarms</h2>
			<div class="tabs" role="tablist">
				<button role="tab" aria-selected={tab === 'active'} onclick={() => (tab = 'active')}>
					Active {#if alarms.active.length}<span class="n">{alarms.active.length}</span>{/if}
				</button>
				<button role="tab" aria-selected={tab === 'history'} onclick={() => (tab = 'history')}>History</button>
			</div>
			<button class="x" onclick={() => (open = false)} aria-label="Close" title="Close (Esc)">×</button>
		</header>
		<div class="body">
			{#if alarms.error}
				<p class="err" role="alert">{alarms.error}</p>
			{/if}
			{#if tab === 'active'}
				<AlarmTable
					alarms={alarms.instances}
					{now}
					onack={(ids, by) => alarms.ack(ids, by)}
					onshelve={(id, until, by) => alarms.shelve(id, Math.max(1, Math.round((until - Date.now()) / 1000)), by)}
					onunshelve={(id, by) => alarms.unshelve(id, by)}
				/>
			{:else}
				<AlarmJournal {events} from={range.from} to={range.to} {loading} onrange={(f, t) => load(f, t)} />
			{/if}
		</div>
	</aside>
{/if}

<style>
	.panel {
		position: absolute;
		top: 12px;
		right: 12px;
		max-height: calc(100% - 24px);
		z-index: 6;
		width: min(760px, calc(100% - 24px));
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		box-shadow: 0 8px 28px rgba(0, 0, 0, 0.4);
	}
	header {
		display: flex;
		align-items: center;
		gap: 16px;
		padding: 10px 12px 10px 16px;
		border-bottom: 1px solid var(--border);
	}
	h2 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 650;
		color: var(--ink);
	}
	.tabs {
		display: flex;
		gap: 2px;
		padding: 2px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--surface-2);
	}
	.tabs button {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 4px 12px;
		border: 0;
		border-radius: 6px;
		background: none;
		color: var(--ink-2);
		font: inherit;
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
	}
	.tabs button[aria-selected='true'] {
		background: var(--s1);
		color: var(--bg);
	}
	.n {
		min-width: 1.4em;
		padding: 0 5px;
		border-radius: 999px;
		background: var(--critical, #e5484d);
		color: #fff;
		font-size: 0.7rem;
		text-align: center;
	}
	.x {
		margin-left: auto;
		border: 0;
		background: none;
		color: var(--muted);
		font-size: 1.3rem;
		line-height: 1;
		padding: 2px 6px;
		border-radius: 6px;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink);
		background: var(--surface-2);
	}
	.body {
		padding: 12px 16px 16px;
		overflow: auto;
		flex: 1;
		min-height: 0;
	}
	.err {
		margin: 0 0 10px;
		font-size: 0.8rem;
		color: var(--critical, #e5484d);
	}
</style>
