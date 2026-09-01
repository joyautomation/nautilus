<script lang="ts">
	// Active-alarm list — `Page/Alarms` → `ia.display.alarmstatustable` in the
	// source Perspective view. Columns, widths, and default sort come
	// verbatim from that view (docs/design/alarms.md §5): Priority | Active
	// Time (sort desc default) | State | Label | Pipeline | Ack Time | Ack
	// User. ISA-18.2 state colors from ../alarms.ts (`STATE_META`) — never
	// color alone, every state also carries a label. Props in, callbacks out:
	// this component never calls fetch: it Emits `onack`/`onshelve`/
	// `onunshelve` for the host (or `alarms.svelte.ts`'s `AlarmClient`) to
	// carry out. Simple pagination stands in for virtualization at 2,000+ rows
	// — a fixed page size keeps row count, and therefore DOM size, bounded.
	import { untrack } from 'svelte';
	import type { AlarmInstance, AlarmState, Priority } from '../alarms.svelte.js';
	import { DEFAULT_SHELVE_TIMES_S, PRIORITY_META, PRIORITY_ORDER, STATE_META } from '../alarms.svelte.js';
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';
	import Icon from './Icon.svelte';
	import { confirm } from '../confirm.svelte.js';
	import { getOperator } from '../confirm.js';
	import { ackLine, worstFirst } from './AckButton.svelte';

	type SortKey = 'priority' | 'activeTime' | 'state' | 'name' | 'ackTime';

	let {
		alarms,
		sites,
		now = Date.now(),
		onack,
		onshelve,
		onunshelve,
		shelveTimes = DEFAULT_SHELVE_TIMES_S,
		operator = '',
		onselect,
		pageSize = 100,
		confirmAck = false
	}: {
		/** Full instance list — active, unack-RTN, and shelved together (e.g.
		 * `alarmClient.instances`). Shelved rows are hidden by default; toggle
		 * "show shelved" to include them. */
		alarms: AlarmInstance[];
		/** Known site/group codes for the filter dropdown; falls back to the
		 * distinct `site` values seen in `alarms`. */
		sites?: string[];
		now?: number;
		/** `ids` is `['*']` for "ack everything" (mirrors `Engine.Ack`'s
		 * nil/["*"] convention), else the selected instance ids. */
		onack: (ids: string[], by: string) => void;
		/** `until` is epoch ms, matching every other timestamp in this kit. */
		onshelve: (id: string, until: number, by: string) => void;
		onunshelve?: (id: string, by: string) => void;
		/** Seconds — the Perspective table's shelvingTimes list, verbatim. */
		shelveTimes?: number[];
		/** Prefills the ack/shelve "by" field — nautilus has one token, not
		 * user accounts, so the HMI supplies the operator name (brief §3). */
		operator?: string;
		/** Row click (outside the checkbox) — faceplate navigation. */
		onselect?: (instance: AlarmInstance) => void;
		pageSize?: number;
		/** Route Ack and Ack All through `ConfirmDialog` first — the alarms
		 *  enumerated worst first, the operator name editable, both paths
		 *  confirming (a single row too: there is no role gate, and the record
		 *  is unauthenticated and permanent). Default `false`, so nothing
		 *  changes for an existing caller; mount `<ConfirmDialog/>` once and
		 *  turn it on. */
		confirmAck?: boolean;
	} = $props();

	let sortKey = $state<SortKey>('activeTime');
	let sortDir = $state<'asc' | 'desc'>('desc');
	let filterPriority = $state<Priority | ''>('');
	let filterState = $state<AlarmState | ''>('');
	let filterSite = $state('');
	let filterText = $state('');
	let showShelved = $state(false);
	let page = $state(0);
	let selected = $state<Set<string>>(new Set());

	let shelveOpen = $state(false);
	// Seeded once — `openShelveModal` re-seeds `shelveSeconds` from the
	// current `shelveTimes` on each open, and `byField` is a scratch edit
	// buffer the operator can adjust before confirming, not a live mirror.
	let shelveSeconds = $state(untrack(() => shelveTimes[0] ?? 300));
	let byField = $state(untrack(() => operator));
	$effect(() => {
		byField = operator;
	});

	const siteOptions = $derived.by(() => {
		if (sites?.length) return sites;
		return [...new Set(alarms.map((a) => a.site).filter((s): s is string => !!s))].sort();
	});

	function stateRank(s: AlarmState): number {
		// Operationally interesting first: needs-ack, then active, then quiet.
		const order: AlarmState[] = ['unack-active', 'unack-rtn', 'ack-active', 'shelved', 'suppressed', 'normal'];
		const i = order.indexOf(s);
		return i < 0 ? order.length : i;
	}

	const filtered = $derived.by(() => {
		const text = filterText.trim().toLowerCase();
		return alarms.filter((a) => {
			if (!showShelved && a.state === 'shelved') return false;
			if (filterPriority && a.priority !== filterPriority) return false;
			if (filterState && a.state !== filterState) return false;
			if (filterSite && a.site !== filterSite) return false;
			if (text) {
				const hay = `${a.name} ${a.tag} ${a.id} ${a.site ?? ''} ${a.area ?? ''}`.toLowerCase();
				if (!hay.includes(text)) return false;
			}
			return true;
		});
	});

	const sorted = $derived.by(() => {
		const dir = sortDir === 'asc' ? 1 : -1;
		return [...filtered].sort((a, b) => {
			let d = 0;
			switch (sortKey) {
				case 'priority':
					d = PRIORITY_ORDER.indexOf(a.priority) - PRIORITY_ORDER.indexOf(b.priority);
					break;
				case 'activeTime':
					d = (a.activeMs ?? 0) - (b.activeMs ?? 0);
					break;
				case 'state':
					d = stateRank(a.state) - stateRank(b.state);
					break;
				case 'ackTime':
					d = (a.ackMs ?? 0) - (b.ackMs ?? 0);
					break;
				case 'name':
					d = a.name.localeCompare(b.name);
					break;
			}
			if (d === 0) d = a.name.localeCompare(b.name);
			return d * dir;
		});
	});

	const pageCount = $derived(Math.max(1, Math.ceil(sorted.length / pageSize)));
	$effect(() => {
		if (page > pageCount - 1) page = Math.max(0, pageCount - 1);
	});
	const pageRows = $derived(sorted.slice(page * pageSize, page * pageSize + pageSize));

	const SORT_LABEL: Record<SortKey, string> = {
		activeTime: 'Active time',
		priority: 'Priority',
		state: 'State',
		name: 'Label',
		ackTime: 'Ack time'
	};

	/** Newest first for time; otherwise ascending — the header's first-click
	 * direction, shared with the stacked layout's sort select. */
	function setSort(key: SortKey) {
		sortKey = key;
		sortDir = key === 'activeTime' ? 'desc' : 'asc';
	}
	function sortBy(key: SortKey) {
		if (sortKey === key) sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		else setSort(key);
	}
	function flipSort() {
		sortDir = sortDir === 'asc' ? 'desc' : 'asc';
	}

	function toggleOne(id: string, ev: Event) {
		ev.stopPropagation();
		const next = new Set(selected);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		selected = next;
	}

	const pageAllSelected = $derived(pageRows.length > 0 && pageRows.every((r) => selected.has(r.id)));
	function togglePage() {
		const next = new Set(selected);
		if (pageAllSelected) for (const r of pageRows) next.delete(r.id);
		else for (const r of pageRows) next.add(r.id);
		selected = next;
	}

	const unackedCount = $derived(alarms.filter((a) => a.state === 'unack-active' || a.state === 'unack-rtn').length);

	/** Ask before acking, when `confirmAck` is on. Returns the name to record. */
	async function askAck(subject: AlarmInstance[], title: string): Promise<string | null> {
		if (!confirmAck) return byField || operator;
		const ok = await confirm({
			title,
			items: worstFirst(subject).map(ackLine),
			confirmLabel: 'Acknowledge',
			operator: true,
			note: 'Acknowledgement is permanent and unauthenticated — the name above is the whole record.'
		});
		if (!ok) return null;
		return getOperator() || byField || operator;
	}

	async function ackSelected() {
		if (!selected.size) return;
		const ids = [...selected];
		const subject = alarms.filter((a) => selected.has(a.id));
		const by = await askAck(
			subject,
			`Acknowledge ${ids.length} alarm${ids.length === 1 ? '' : 's'}?`
		);
		if (by === null) return;
		onack(ids, by);
		selected = new Set();
	}
	async function ackAll() {
		const subject = alarms.filter((a) => a.state === 'unack-active' || a.state === 'unack-rtn');
		const by = await askAck(subject, `Acknowledge all ${subject.length} unacknowledged alarms?`);
		if (by === null) return;
		onack(['*'], by);
		selected = new Set();
	}
	function unshelveSelected() {
		if (!onunshelve) return;
		for (const id of selected) onunshelve(id, byField || operator);
		selected = new Set();
	}

	function openShelveModal() {
		if (!selected.size) return;
		shelveSeconds = shelveTimes[0] ?? 300;
		shelveOpen = true;
	}
	function confirmShelve() {
		const until = now + shelveSeconds * 1000;
		for (const id of selected) onshelve(id, until, byField || operator);
		selected = new Set();
		shelveOpen = false;
	}

	function fmtDuration(ms: number): string {
		const s = Math.max(0, Math.floor(ms / 1000));
		if (s < 60) return `${s}s`;
		if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
		if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
		return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
	}
	function fmtTime(ms: number | undefined): string {
		if (!ms) return '—';
		return new Date(ms).toLocaleString(undefined, {
			month: 'short',
			day: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit'
		});
	}
	function fmtShelveOpt(s: number): string {
		if (s < 3600) return `${s / 60}m`;
		return `${s / 3600}h`;
	}
</script>

<div class="wrap">
	<div class="toolbar">
		<div class="filters">
			<select bind:value={filterPriority} aria-label="Filter by priority">
				<option value="">All priorities</option>
				{#each [...PRIORITY_ORDER].reverse() as p (p)}
					<option value={p}>{PRIORITY_META[p].label}</option>
				{/each}
			</select>
			<select bind:value={filterState} aria-label="Filter by state">
				<option value="">All states</option>
				{#each Object.keys(STATE_META) as s (s)}
					<option value={s}>{STATE_META[s as AlarmState].label}</option>
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
			<input type="search" placeholder="Search name / tag…" bind:value={filterText} aria-label="Search alarms" />
			<label class="chip">
				<input type="checkbox" bind:checked={showShelved} />
				show shelved
			</label>
			<!-- Only shown in the stacked layout, where the sortable header is
			     out of sight — same `sortKey`/`sortDir` the header drives. -->
			<div class="sortctl" role="group" aria-label="Sort">
				<select
					value={sortKey}
					onchange={(e) => setSort(e.currentTarget.value as SortKey)}
					aria-label="Sort by"
				>
					{#each Object.keys(SORT_LABEL) as k (k)}
						<option value={k}>{SORT_LABEL[k as SortKey]}</option>
					{/each}
				</select>
				<button
					type="button"
					class="dir"
					onclick={flipSort}
					aria-label={sortDir === 'desc' ? 'Sorted descending — switch to ascending' : 'Sorted ascending — switch to descending'}
				>
					{sortDir === 'desc' ? '▾' : '▴'}
				</button>
			</div>
		</div>

		<div class="actions">
			<span class="count">{unackedCount} unack · {sorted.length} shown</span>
			<Button size="sm" icon="check" disabled={!selected.size} onclick={ackSelected}>
				Ack ({selected.size})
			</Button>
			<Button size="sm" variant="secondary" icon="check-circle" onclick={ackAll}>Ack All</Button>
			<Button size="sm" icon="clock" disabled={!selected.size} onclick={openShelveModal}>Shelve</Button>
			{#if onunshelve}
				<Button size="sm" variant="ghost" icon="arrow-path" disabled={!selected.size} onclick={unshelveSelected}>
					Unshelve
				</Button>
			{/if}
		</div>
	</div>

	<div class="tablescroll">
		<table>
			<thead>
				<tr>
					<th class="sel">
						<input
							type="checkbox"
							checked={pageAllSelected}
							onclick={togglePage}
							aria-label="Select all rows on this page"
						/>
					</th>
					<th class="col-priority">
						<button type="button" class="sort" onclick={() => sortBy('priority')}>Priority</button>
					</th>
					<th class="col-active">
						<button type="button" class="sort" onclick={() => sortBy('activeTime')}>
							Active Time {sortKey === 'activeTime' ? (sortDir === 'desc' ? '▾' : '▴') : ''}
						</button>
					</th>
					<th class="col-state">
						<button type="button" class="sort" onclick={() => sortBy('state')}>State</button>
					</th>
					<th class="col-label">
						<button type="button" class="sort" onclick={() => sortBy('name')}>Label</button>
					</th>
					<th class="col-pipeline">Pipeline</th>
					<th class="col-ack">
						<button type="button" class="sort" onclick={() => sortBy('ackTime')}>Ack Time</button>
					</th>
					<th class="col-ackuser">Ack User</th>
				</tr>
			</thead>
			<tbody>
				{#each pageRows as a (a.id)}
					{@const meta = STATE_META[a.state]}
					<tr
						class:selected={selected.has(a.id)}
						class:flash={meta.flash}
						onclick={() => onselect?.(a)}
						aria-live={a.priority === 'critical' || a.priority === 'high'
							? a.state === 'unack-active'
								? 'assertive'
								: undefined
							: undefined}
					>
						<td class="sel" onclick={(e) => toggleOne(a.id, e)}>
							<input type="checkbox" checked={selected.has(a.id)} onclick={(e) => toggleOne(a.id, e)} />
						</td>
						<td class="col-priority">
							<span class="prio" style="--c: {PRIORITY_META[a.priority].color}" title={PRIORITY_META[a.priority].label}>
								<span aria-hidden="true">{PRIORITY_META[a.priority].glyph}</span>
							</span>
						</td>
						<td class="col-active num" title={fmtTime(a.activeMs)}>
							{a.activeMs ? fmtDuration(now - a.activeMs) : '—'}
						</td>
						<td class="col-state">
							<span class="state" style="--c: {meta.color}">
								<span class="dot" aria-hidden="true"></span>
								{meta.label}
							</span>
						</td>
						<td class="col-label" title={a.tag}>{a.name}</td>
						<td class="col-pipeline">{a.class ?? ([a.site, a.area].filter(Boolean).join(' / ') || '—')}</td>
						<td class="col-ack num" class:blank={!a.ackMs} data-label="Acked">{fmtTime(a.ackMs)}</td>
						<td class="col-ackuser" class:blank={!a.ackBy} data-label="by">{a.ackBy ?? '—'}</td>
					</tr>
				{:else}
					<tr class="empty"><td colspan="8">No alarms match these filters.</td></tr>
				{/each}
			</tbody>
		</table>
	</div>

	{#if pageCount > 1}
		<div class="pager">
			<Button size="sm" variant="ghost" disabled={page === 0} onclick={() => (page -= 1)}>Prev</Button>
			<span>page {page + 1} / {pageCount}</span>
			<Button size="sm" variant="ghost" disabled={page >= pageCount - 1} onclick={() => (page += 1)}>Next</Button>
		</div>
	{/if}
</div>

<Modal bind:open={shelveOpen} title="Shelve {selected.size} alarm(s)" size="sm">
	{#snippet children()}
		<label class="field">
			<span>Duration</span>
			<select bind:value={shelveSeconds}>
				{#each shelveTimes as s (s)}
					<option value={s}>{fmtShelveOpt(s)}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			<span>Acknowledged by</span>
			<input type="text" bind:value={byField} placeholder="operator name" />
		</label>
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (shelveOpen = false)}>Cancel</Button>
		<Button variant="primary" onclick={confirmShelve}>Shelve</Button>
	{/snippet}
</Modal>

<style>
	.wrap {
		display: grid;
		gap: var(--space-2);
	}
	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	/* Both toolbar rows wrap, and every control may shrink below its
	   intrinsic width — three selects fit a phone in one or two rows and
	   nothing runs past the container. */
	.toolbar > * {
		min-width: 0;
		max-width: 100%;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		flex: 1 1 auto;
	}
	.filters > select {
		min-width: 0;
		flex: 1 1 7.5rem;
	}
	.filters > input[type='search'] {
		min-width: 0;
		flex: 1 1 10rem;
	}
	.sortctl {
		display: none; /* stacked layout only — see the container query */
		align-items: center;
		gap: var(--space-1);
		flex: 1 1 auto;
	}
	.sortctl select {
		min-width: 0;
		flex: 1 1 auto;
	}
	.sortctl .dir {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--ink-2);
		font: inherit;
		font-size: var(--font-xs);
		padding: var(--space-1) var(--space-2);
		cursor: pointer;
	}
	select,
	input[type='search'],
	input[type='text'] {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--ink);
		font: inherit;
		font-size: var(--font-xs);
		padding: var(--space-1) var(--space-2);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		font-size: var(--font-2xs);
		color: var(--muted);
		white-space: nowrap;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.actions .count {
		font-size: var(--font-2xs);
		color: var(--muted);
		margin-right: var(--space-1);
		white-space: nowrap;
	}
	.tablescroll {
		overflow-x: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius);
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
		padding: var(--space-2);
		border-bottom: 1px solid var(--border);
		background: var(--surface-2);
		white-space: nowrap;
	}
	.sort {
		background: none;
		border: none;
		font: inherit;
		font-weight: inherit;
		letter-spacing: inherit;
		text-transform: inherit;
		color: inherit;
		cursor: pointer;
		padding: 0;
	}
	.sort:hover {
		color: var(--ink);
	}
	td {
		padding: var(--space-2);
		border-bottom: 1px solid var(--border);
		color: var(--ink-2);
		/* Fixed min-height via line-height-ish padding keeps a per-frame
		   update from reflowing the list. */
	}
	tbody tr {
		cursor: pointer;
	}
	tbody tr:hover {
		background: var(--hover, var(--surface-2));
	}
	tbody tr.selected {
		background: color-mix(in srgb, var(--accent) 10%, transparent);
	}
	tbody tr.flash {
		animation: rowflash 1.1s ease-in-out infinite;
	}
	@keyframes rowflash {
		0%,
		100% {
			background: transparent;
		}
		50% {
			background: color-mix(in srgb, var(--crit) 12%, transparent);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		tbody tr.flash {
			animation: none;
			background: color-mix(in srgb, var(--crit) 10%, transparent);
		}
	}
	tr.empty td {
		text-align: center;
		color: var(--muted);
		padding: var(--space-6) var(--space-2);
	}
	/* Column widths ride on the header cells (the column follows) so the
	   stacked layout below can lay the body cells out freely. */
	th.sel {
		width: 32px;
	}
	td.sel {
		cursor: default;
	}
	th.col-priority {
		width: 70px;
	}
	th.col-active {
		width: 140px;
	}
	th.col-state {
		width: 160px;
	}
	th.col-label {
		min-width: 220px;
	}
	th.col-pipeline {
		width: 160px;
	}
	th.col-ack {
		width: 150px;
	}
	th.col-ackuser {
		width: 120px;
	}
	.num {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
	}
	.prio {
		display: inline-flex;
		width: 20px;
		height: 20px;
		align-items: center;
		justify-content: center;
		border-radius: var(--radius-sm);
		color: var(--c);
		background: color-mix(in srgb, var(--c) var(--tint-strength), transparent);
	}
	.state {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		color: var(--c);
		font-weight: 600;
	}
	.state .dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--c);
		flex: none;
	}
	.pager {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-2);
		font-size: var(--font-2xs);
		color: var(--muted);
	}
	.field {
		display: grid;
		gap: var(--space-1);
		font-size: var(--font-xs);
		color: var(--ink-2);
	}
	.field select,
	.field input {
		width: 100%;
	}

	/* ── stacked cards ──────────────────────────────────────────────────
	   Below 640px of the component's OWN width (a container query, so a
	   table in a narrow panel on a wide screen stacks too) the 8-column
	   grid gives way to one card per row. Same DOM — the `<tr>`/`<td>`
	   are what a11y and the tests expect — the row becomes a grid and each
	   cell takes its place by column class:
	     line 1  priority glyph · label (wraps, never ellipsised) · select
	     line 2  state · active age · pipeline (site/area)
	     line 3  ack time · ack user — only when there is one
	   The header is hidden from sight, not from the tree (its sort buttons
	   stay reachable); the toolbar's `.sortctl` shows in its stead. */
	.wrap {
		container-type: inline-size;
	}
	@container (max-width: 640px) {
		.sortctl {
			display: inline-flex;
		}
		.tablescroll {
			position: relative;
			overflow: hidden;
		}
		table,
		tbody {
			display: block;
		}
		thead {
			display: block;
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip-path: inset(50%);
			white-space: nowrap;
		}
		tbody tr {
			display: grid;
			grid-template-columns: max-content minmax(0, 1fr) max-content fit-content(40%);
			gap: var(--space-1) var(--space-2);
			align-items: center;
			padding: var(--space-2);
			border-bottom: 1px solid var(--border);
		}
		tbody td {
			display: block;
			padding: 0;
			border: 0;
			min-width: 0;
		}
		td.col-priority {
			grid-area: 1 / 1;
		}
		td.col-label {
			grid-area: 1 / 2 / 2 / 4;
			color: var(--ink);
			font-weight: 600;
			white-space: normal;
			overflow-wrap: anywhere;
		}
		td.sel {
			grid-area: 1 / 4;
			justify-self: end;
			display: grid;
			place-items: center;
		}
		td.col-state {
			grid-area: 2 / 1 / 3 / 3;
		}
		td.col-active {
			grid-area: 2 / 3;
		}
		td.col-pipeline {
			grid-area: 2 / 4;
			justify-self: end;
			text-align: right;
			color: var(--muted);
			white-space: normal;
			overflow-wrap: anywhere;
		}
		td.col-ack {
			grid-area: 3 / 1 / 4 / 3;
		}
		td.col-ackuser {
			grid-area: 3 / 3 / 4 / 5;
		}
		td.col-ack::before,
		td.col-ackuser::before {
			content: attr(data-label) ' ';
			font-family: var(--font);
			color: var(--muted);
		}
		td.blank {
			display: none;
		}
		tr.empty,
		tr.empty td {
			display: block;
		}
	}

	/* Coarse pointer (touch/pen): the select cell grows to the minimum hit
	   area — the cell, not just the 13px box, toggles the row. Desktop
	   density (mouse/trackpad) is untouched. */
	@media (pointer: coarse) {
		td.sel {
			min-width: var(--tap);
			min-height: var(--tap);
		}
		.sortctl .dir {
			min-height: var(--tap);
			min-width: var(--tap);
		}
	}
</style>
