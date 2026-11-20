<script lang="ts">
	// The audit journal (#709): who did what, to what, with what result.
	// Reads GET /api/audit through `AuditClient` (the kit's fetch, so a
	// signed-in browser needs nothing more). Filters are the journal's own:
	// exact matches on user, action, target and result, a time range, and a
	// limit; the controller sends newest first and says when it cut the list.
	//
	// The journal needs the audit permission. A role without it is told so
	// rather than shown an empty table.
	import { onMount } from 'svelte';
	import { AuditClient, actorText, changeText, type AuditEntry, type AuditFilter } from '../audit.js';
	import { deniedFor } from '../auth.svelte.js';
	import Button from './Button.svelte';
	import StatusPill from './StatusPill.svelte';

	let {
		client = new AuditClient(),
		filter = $bindable({}),
		limit = 200,
		auto = true
	}: {
		/** Default: `new AuditClient()` (/api/audit through the kit's fetch). */
		client?: AuditClient;
		/** The filter the toolbar edits; bind it to keep it in the URL or a store. */
		filter?: AuditFilter;
		/** At most this many rows a read. */
		limit?: number;
		/** Read once on mount. Off, the host calls `load()` through `bind:this`. */
		auto?: boolean;
	} = $props();

	let entries = $state<AuditEntry[]>([]);
	let truncated = $state(false);
	let error = $state('');
	let loading = $state(false);

	// Ahead of time, so a role without audit is told before it asks.
	const denied = $derived(deniedFor('audit'));

	export async function load() {
		loading = true;
		const r = await client.query({ ...filter, limit });
		loading = false;
		if (r.ok) {
			entries = r.entries;
			truncated = r.truncated;
			error = '';
		} else {
			entries = [];
			truncated = false;
			error = r.message;
		}
	}

	onMount(() => {
		if (auto && !denied) void load();
	});

	const RESULT_KIND: Record<string, 'good' | 'warning' | 'serious' | 'critical' | 'off'> = {
		ok: 'good',
		refused: 'serious',
		error: 'critical',
		unknown: 'warning'
	};

	function fmtTime(ts: string): string {
		const d = new Date(ts);
		return Number.isFinite(d.getTime()) ? d.toLocaleString() : ts;
	}

	function detailText(e: AuditEntry): string {
		if (!e.detail) return '';
		return Object.entries(e.detail)
			.map(([k, v]) => `${k}: ${typeof v === 'string' ? v : JSON.stringify(v)}`)
			.join(' · ');
	}
</script>

<div class="wrap">
	{#if denied}
		<p class="msg" role="status">The audit journal needs the audit permission: {denied}.</p>
	{:else}
		<form
			class="filters"
			onsubmit={(e) => {
				e.preventDefault();
				void load();
			}}
		>
			<input type="search" placeholder="User" bind:value={filter.user} aria-label="Filter by user" />
			<input type="search" placeholder="Action (tag.write)" bind:value={filter.action} aria-label="Filter by action" />
			<input type="search" placeholder="Target" bind:value={filter.target} aria-label="Filter by target" />
			<select bind:value={filter.result} aria-label="Filter by result">
				<option value="">Any result</option>
				<option value="ok">ok</option>
				<option value="refused">refused</option>
				<option value="error">error</option>
				<option value="unknown">unknown</option>
			</select>
			<Button size="sm" type="submit" icon="arrow-path" loading={loading}>Read</Button>
		</form>

		{#if error}<p class="msg err" role="alert">{error}</p>{/if}

		<div class="tablescroll">
			<table>
				<thead>
					<tr>
						<th>Time</th>
						<th>Who</th>
						<th>Action</th>
						<th>Target</th>
						<th>Change</th>
						<th>Result</th>
						<th>Detail</th>
					</tr>
				</thead>
				<tbody>
					{#each entries as e (e.id)}
						<tr>
							<td class="num">{fmtTime(e.ts)}</td>
							<td title={`${e.user} via ${e.source}${e.from ? ` from ${e.from}` : ''}`}>{actorText(e)}</td>
							<td class="mono">{e.action}</td>
							<td class="mono">{e.target ?? ''}</td>
							<td class="mono">{changeText(e)}</td>
							<td><StatusPill kind={RESULT_KIND[e.result] ?? 'off'} label={e.result} /></td>
							<td class="detail">{detailText(e)}</td>
						</tr>
					{:else}
						<tr class="empty"><td colspan="7">{loading ? 'Reading…' : 'No entries match.'}</td></tr>
					{/each}
				</tbody>
			</table>
		</div>
		{#if truncated}
			<p class="msg">Showing the newest {entries.length}; there are more. Narrow the filter to see them.</p>
		{/if}
	{/if}
</div>

<style>
	.wrap {
		display: grid;
		gap: 10px;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.filters > input,
	.filters > select {
		min-width: 0;
		flex: 1 1 8rem;
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
	td.num,
	td.mono {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
	}
	td.detail {
		white-space: normal;
		color: var(--muted);
		font-size: var(--font-2xs);
	}
	tr.empty td {
		text-align: center;
		color: var(--muted);
		padding: 16px;
	}
	.msg {
		margin: 0;
		font-size: var(--font-xs);
		color: var(--muted);
	}
	.msg.err {
		color: var(--crit);
	}
</style>
