<script lang="ts">
	// Alarm history — `Page/AlarmJournal` → `ia.display.alarmjournaltable` in
	// the source Perspective view. A time-range picker (last hour/24h/7d, or
	// custom) plus client-side filters over whatever `events` the host handed
	// in, and a CSV export of the filtered rows. Per the brief (§5): Name,
	// State, Ack User enabled; Event ID and Priority disabled — this renders
	// Time / Alarm / Event / State After / Operator. Props in, callbacks out:
	// fetching the range lives in the host (or `alarms.svelte.ts`'s
	// `AlarmClient.journal()`) — this component only asks for a new range via
	// `onrange` and renders whatever `events` it is then given.
	import { untrack } from 'svelte';
	import type { AlarmEvent, AlarmEventKind, Priority } from '../alarms.js';
	import { PRIORITY_META } from '../alarms.js';
	import Button from './Button.svelte';

	const EVENT_LABEL: Record<AlarmEventKind, string> = {
		active: 'Activated',
		rtn: 'Returned to normal',
		ack: 'Acknowledged',
		shelve: 'Shelved',
		unshelve: 'Unshelved',
		suppress: 'Suppressed',
		unsuppress: 'Unsuppressed'
	};

	// The state an event kind leaves the alarm in — for the "State After"
	// column, which the brief's source view enables.
	const STATE_AFTER: Record<AlarmEventKind, string> = {
		active: 'unack-active',
		rtn: 'unack-rtn',
		ack: 'ack-active / normal',
		shelve: 'shelved',
		unshelve: 'restored',
		suppress: 'suppressed',
		unsuppress: 'restored'
	};

	type Preset = '1h' | '24h' | '7d' | 'custom';

	let {
		events,
		from,
		to,
		onrange,
		sites,
		loading = false
	}: {
		events: AlarmEvent[];
		/** Epoch ms range currently loaded — reflected in the picker. */
		from: number;
		to: number;
		/** Requests a new range; the host re-fetches and passes fresh `events`. */
		onrange: (from: number, to: number) => void;
		sites?: string[];
		loading?: boolean;
	} = $props();

	let preset = $state<Preset>('24h');
	// Seeded once from the initial `from`/`to` props — the picker is the
	// source of truth for the range from here on (via `onrange`), so this is
	// deliberately not reactive to later prop changes.
	let customFrom = $state(untrack(() => toLocalInput(from)));
	let customTo = $state(untrack(() => toLocalInput(to)));

	let filterKind = $state<AlarmEventKind | ''>('');
	let filterPriority = $state<Priority | ''>('');
	let filterSite = $state('');
	let filterText = $state('');

	function toLocalInput(ms: number): string {
		const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60000);
		return d.toISOString().slice(0, 16);
	}

	function applyPreset(p: Preset) {
		preset = p;
		if (p === 'custom') return;
		const now = Date.now();
		const spanMs = p === '1h' ? 3600_000 : p === '24h' ? 86_400_000 : 7 * 86_400_000;
		customFrom = toLocalInput(now - spanMs);
		customTo = toLocalInput(now);
		onrange(now - spanMs, now);
	}

	function applyCustom() {
		const f = new Date(customFrom).getTime();
		const t = new Date(customTo).getTime();
		if (!isFinite(f) || !isFinite(t)) return;
		onrange(f, t);
	}

	const siteOptions = $derived.by(() => {
		if (sites?.length) return sites;
		return [...new Set(events.map((e) => e.site).filter((s): s is string => !!s))].sort();
	});

	const filtered = $derived.by(() => {
		const text = filterText.trim().toLowerCase();
		return events
			.filter((e) => {
				if (filterKind && e.kind !== filterKind) return false;
				if (filterPriority && e.priority !== filterPriority) return false;
				if (filterSite && e.site !== filterSite) return false;
				if (text && !`${e.name} ${e.id}`.toLowerCase().includes(text)) return false;
				return true;
			})
			.sort((a, b) => b.ts - a.ts);
	});

	function fmtTime(ms: number): string {
		return new Date(ms).toLocaleString(undefined, {
			year: 'numeric',
			month: 'short',
			day: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit'
		});
	}

	function csvCell(v: unknown): string {
		const s = String(v ?? '');
		return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
	}

	function exportCsv() {
		const header = ['time', 'alarm', 'id', 'event', 'priority', 'site', 'state_after', 'operator'];
		const rows = filtered.map((e) =>
			[fmtTime(e.ts), e.name, e.id, EVENT_LABEL[e.kind], e.priority ?? '', e.site ?? '', STATE_AFTER[e.kind], e.by ?? '']
				.map(csvCell)
				.join(',')
		);
		const csv = [header.join(','), ...rows].join('\n');
		const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `alarm-journal-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.csv`;
		document.body.appendChild(a);
		a.click();
		a.remove();
		URL.revokeObjectURL(url);
	}
</script>

<div class="wrap">
	<div class="toolbar">
		<div class="range">
			<div class="presets" role="group" aria-label="Time range">
				<button type="button" class:active={preset === '1h'} onclick={() => applyPreset('1h')}>Last hour</button>
				<button type="button" class:active={preset === '24h'} onclick={() => applyPreset('24h')}>Last 24h</button>
				<button type="button" class:active={preset === '7d'} onclick={() => applyPreset('7d')}>Last 7d</button>
				<button type="button" class:active={preset === 'custom'} onclick={() => (preset = 'custom')}>Custom</button>
			</div>
			{#if preset === 'custom'}
				<div class="custom">
					<input type="datetime-local" bind:value={customFrom} aria-label="From" />
					<span>to</span>
					<input type="datetime-local" bind:value={customTo} aria-label="To" />
					<Button size="sm" onclick={applyCustom}>Apply</Button>
				</div>
			{/if}
		</div>

		<div class="filters">
			<select bind:value={filterKind} aria-label="Filter by event">
				<option value="">All events</option>
				{#each Object.entries(EVENT_LABEL) as [k, label] (k)}
					<option value={k}>{label}</option>
				{/each}
			</select>
			<select bind:value={filterPriority} aria-label="Filter by priority">
				<option value="">All priorities</option>
				{#each Object.entries(PRIORITY_META) as [p, m] (p)}
					<option value={p}>{m.label}</option>
				{/each}
			</select>
			{#if siteOptions.length}
				<select bind:value={filterSite} aria-label="Filter by site">
					<option value="">All sites</option>
					{#each siteOptions as s (s)}
						<option value={s}>{s}</option>
					{/each}
				</select>
			{/if}
			<input type="search" placeholder="Search…" bind:value={filterText} aria-label="Search events" />
			<Button size="sm" variant="secondary" icon="document" onclick={exportCsv} disabled={!filtered.length}>
				Export CSV
			</Button>
		</div>
	</div>

	<div class="tablescroll">
		<table>
			<thead>
				<tr>
					<th>Time</th>
					<th>Alarm</th>
					<th>Event</th>
					<th>State After</th>
					<th>Operator</th>
				</tr>
			</thead>
			<tbody>
				{#if loading}
					<tr class="empty"><td colspan="5">Loading…</td></tr>
				{:else}
					{#each filtered as e (e.ts + ':' + e.id + ':' + e.kind)}
						<tr>
							<td class="num">{fmtTime(e.ts)}</td>
							<td title={e.id}>{e.name}</td>
							<td>{EVENT_LABEL[e.kind]}</td>
							<td>{STATE_AFTER[e.kind]}</td>
							<td>{e.by ?? '—'}</td>
						</tr>
					{:else}
						<tr class="empty"><td colspan="5">No events in this range.</td></tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>
</div>

<style>
	.wrap {
		display: grid;
		gap: 10px;
	}
	.toolbar {
		display: grid;
		gap: 8px;
	}
	.range {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
	}
	.presets {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 7px;
		overflow: hidden;
	}
	.presets button {
		background: var(--surface-2);
		border: none;
		border-right: 1px solid var(--border);
		color: var(--ink-2);
		font: inherit;
		font-size: var(--font-xs);
		padding: 5px 10px;
		cursor: pointer;
	}
	.presets button:last-child {
		border-right: none;
	}
	.presets button:hover {
		color: var(--ink);
	}
	.presets button.active {
		background: var(--accent);
		color: #fff;
	}
	.custom {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: var(--font-2xs);
		color: var(--muted);
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
	}
	select,
	input[type='search'],
	input[type='datetime-local'] {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--ink);
		font: inherit;
		font-size: var(--font-xs);
		padding: 5px 8px;
	}
	.tablescroll {
		overflow-x: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--font-xs);
	}
	th {
		text-align: left;
		font-size: var(--font-2xs);
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--muted);
		padding: 8px 10px;
		border-bottom: 1px solid var(--border);
		background: var(--surface-2);
		white-space: nowrap;
	}
	td {
		padding: 7px 10px;
		border-bottom: 1px solid var(--border);
		color: var(--ink-2);
		white-space: nowrap;
	}
	td.num {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
	}
	tr.empty td {
		text-align: center;
		color: var(--muted);
		padding: 24px 10px;
		white-space: normal;
	}
</style>
