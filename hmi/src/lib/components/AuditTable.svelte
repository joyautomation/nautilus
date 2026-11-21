<script lang="ts">
	// The audit journal (#709): who did what, to what, with what result.
	// Reads GET /api/audit through `AuditClient` (the kit's fetch, so a
	// signed-in browser needs nothing more). Filters are the journal's own:
	// exact matches on user, action, target and result, a time range, and a
	// limit; the controller sends newest first and says when it cut the list.
	//
	// The Change column says known actions in a few words (a substitute's
	// value, until when; the alarms an acknowledge-all took); what the entry
	// holds in full is under "raw" on that cell. Below 640 px of its own
	// width the table stacks, one card per entry, instead of scrolling
	// sideways.
	//
	// The journal needs the audit permission. A role without it is told so
	// rather than shown an empty table.
	import { onMount } from 'svelte';
	import { AuditClient, actorText, changeText, changeRaw, type AuditEntry, type AuditFilter } from '../audit.js';
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
			<!-- svelte-ignore a11y_no_redundant_roles -->
			<table role="table">
				<!-- svelte-ignore a11y_no_redundant_roles -->
				<thead role="rowgroup">
					<!-- svelte-ignore a11y_no_redundant_roles -->
					<tr role="row">
						<th role="columnheader">Time</th>
						<th role="columnheader">Who</th>
						<th role="columnheader">Action</th>
						<th role="columnheader">Target</th>
						<th role="columnheader">Change</th>
						<th role="columnheader">Result</th>
						<th role="columnheader">Detail</th>
					</tr>
				</thead>
				<!-- svelte-ignore a11y_no_redundant_roles -->
				<tbody role="rowgroup">
					{#each entries as e (e.id)}
						{@const raw = changeRaw(e)}
						{@const change = changeText(e)}
						<!-- svelte-ignore a11y_no_redundant_roles -->
						<tr role="row">
							<td role="cell" class="num" data-label="Time">{fmtTime(e.ts)}</td>
							<td role="cell" data-label="Who" title={`${e.user} via ${e.source}${e.from ? ` from ${e.from}` : ''}`}>{actorText(e)}</td>
							<td role="cell" class="mono" data-label="Action">{e.action}</td>
							<td role="cell" class="mono" data-label="Target">{e.target ?? ''}</td>
							<td role="cell" class="change" class:none={!change && !raw} data-label="Change">
								{#if change}<span class="mono">{change}</span>{/if}
								{#if raw}
									<details>
										<summary>raw</summary>
										<pre>{raw}</pre>
									</details>
								{/if}
							</td>
							<td role="cell" data-label="Result"><StatusPill kind={RESULT_KIND[e.result] ?? 'off'} label={e.result} /></td>
							<td role="cell" class="detail" class:none={!detailText(e)} data-label="Detail">{detailText(e)}</td>
						</tr>
					{:else}
						<tr class="empty"><td role="cell" colspan="7">{loading ? 'Reading…' : 'No entries match.'}</td></tr>
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
		grid-template-columns: minmax(0, 1fr);
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
		container-type: inline-size;
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
	td.detail,
	td.change {
		white-space: normal;
		overflow-wrap: anywhere;
	}
	td.change {
		min-width: 10rem;
		max-width: 26rem;
	}
	td.detail {
		color: var(--muted);
		font-size: var(--font-2xs);
	}
	summary {
		cursor: pointer;
		color: var(--muted);
		font-size: var(--font-2xs);
	}
	pre {
		margin: 4px 0 0;
		font-family: var(--mono);
		font-size: var(--font-2xs);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		color: var(--ink-2);
	}
	tr.empty td {
		text-align: center;
		color: var(--muted);
		padding: 16px;
	}
	/* Stacked: a card per entry, each cell labelled by its column. */
	@container (max-width: 640px) {
		/* Hidden to the eye, kept for assistive tech (display: none would drop it). */
		thead {
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip-path: inset(50%);
		}
		table,
		tbody {
			display: block;
		}
		tr {
			display: block;
			padding: 6px 10px;
			border-bottom: 1px solid var(--border);
		}
		td {
			display: grid;
			grid-template-columns: 5.5rem minmax(0, 1fr);
			gap: 8px;
			align-items: baseline;
			padding: 3px 0;
			border: 0;
			white-space: normal;
			overflow-wrap: anywhere;
			max-width: none;
		}
		td::before {
			content: attr(data-label);
			font-size: var(--font-2xs);
			font-weight: 600;
			letter-spacing: 0.04em;
			text-transform: uppercase;
			color: var(--muted);
		}
		td.none {
			display: none;
		}
		tr.empty td {
			display: block;
		}
		tr.empty td::before {
			content: none;
		}
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
