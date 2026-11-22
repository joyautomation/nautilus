<script lang="ts">
	// The setpoint parameters (#718): what the plant runs against what the
	// manifest's `init:` says, with a badge for where they differ. Reads
	// GET /api/parameters through `ParametersClient` (the kit's fetch).
	//
	// A parameter the plant is ahead on is an operator's edit; it is shown,
	// never reverted from here. For a row the repo is ahead on (a redeploy
	// brought a new init:) or a conflict, "Use repo value" imports the init:
	// through POST /api/parameters/import: the controller first says what
	// would change (a dry run), the operator confirms, then it applies, with
	// the same gates as any write (range, forced, read-only, the config
	// permission and its fresh password). Below 640 px of its own width the
	// table stacks, one card per parameter.
	import { onMount } from 'svelte';
	import {
		ParametersClient,
		driftBadge,
		drifted,
		rangeText,
		takeRepo,
		valueText,
		type Parameter,
		type WriteBack
	} from '../parameters.js';
	import { confirm } from '../confirm.svelte.js';
	import Button from './Button.svelte';
	import StatusPill from './StatusPill.svelte';

	let {
		client = new ParametersClient(),
		auto = true,
		driftOnly = $bindable(false)
	}: {
		/** Default: `new ParametersClient()` (/api/parameters through the kit's fetch). */
		client?: ParametersClient;
		/** Read once on mount. Off, the host calls `load()` through `bind:this`. */
		auto?: boolean;
		/** Show only parameters whose plant value is not the manifest's init:. */
		driftOnly?: boolean;
	} = $props();

	let rows = $state<Parameter[]>([]);
	let writeBack = $state<WriteBack>('off');
	let error = $state('');
	let note = $state('');
	let loading = $state(false);
	let busy = $state(false);

	const shown = $derived(driftOnly ? drifted(rows) : rows);
	const driftCount = $derived(drifted(rows).length);

	let seq = 0;
	export async function load() {
		const mine = ++seq;
		loading = true;
		const r = await client.list();
		if (mine !== seq) return; // a newer read is under way; this one is stale
		loading = false;
		if (r.ok) {
			rows = r.parameters;
			writeBack = r.writeBack;
			error = '';
		} else {
			error = r.message; // keep what was shown; a failed read is not "no parameters"
		}
	}

	onMount(() => {
		if (auto) void load();
	});

	/** Take the repo's value for `which`: dry run, confirm what changes, apply. */
	async function doTake(which: Parameter[]) {
		if (busy) return;
		busy = true;
		note = '';
		error = '';
		try {
			const r = await takeRepo(client, which, (lines) =>
				confirm({
					title: `Use the repo value for ${lines.length} parameter${lines.length === 1 ? '' : 's'}?`,
					body: 'The plant value is replaced by the manifest init:.',
					items: lines,
					maxItems: 0,
					confirmLabel: 'Use repo value',
					danger: true
				})
			);
			if (r.kind === 'error') error = r.message;
			else if (r.kind !== 'declined') note = r.message;
			if (r.kind === 'applied' || r.kind === 'nothing') await load();
		} finally {
			busy = false;
		}
	}

	const canTake = (p: Parameter) => p.state === 'repo-ahead' || p.state === 'conflict';
	// A conflict holds an operator's edit, so a person decides it row by row;
	// only a repo-ahead row (nobody edited it) goes in the bulk button.
	const takeable = $derived(rows.filter((p) => p.state === 'repo-ahead'));

	function who(p: Parameter): string {
		if (!p.editedBy) return '';
		const name = p.editedBy.replace(/^user:/, '');
		return p.editedAtMs ? `${name}, ${new Date(p.editedAtMs).toLocaleString()}` : name;
	}
</script>

<div class="wrap">
	<div class="bar">
		<label class="only">
			<input type="checkbox" bind:checked={driftOnly} />
			Only where plant and repo differ{rows.length ? ` (${driftCount})` : ''}
		</label>
		<span class="wb" title="The manifest's parameters.writeBack">write-back: {writeBack}</span>
		<span class="grow"></span>
		{#if takeable.length > 1}
			<Button size="sm" variant="danger" disabled={busy} onclick={() => doTake(takeable)}>Use all repo-ahead values ({takeable.length})</Button>
		{/if}
		<Button size="sm" icon="arrow-path" loading={loading} onclick={() => load()}>Read</Button>
	</div>

	<p class="msg err" role="alert">{error}</p>
	<p class="msg" role="status">{note}</p>

	<div class="tablescroll">
		<!-- svelte-ignore a11y_no_redundant_roles -->
		<table role="table">
			<!-- svelte-ignore a11y_no_redundant_roles -->
			<thead role="rowgroup">
				<!-- svelte-ignore a11y_no_redundant_roles -->
				<tr role="row">
					<th role="columnheader">Parameter</th>
					<th role="columnheader">Plant</th>
					<th role="columnheader">Repo init</th>
					<th role="columnheader">Range</th>
					<th role="columnheader">State</th>
					<th role="columnheader">Last edit</th>
					<th role="columnheader"><span class="sr">Action</span></th>
				</tr>
			</thead>
			<!-- svelte-ignore a11y_no_redundant_roles -->
			<tbody role="rowgroup">
				{#each shown as p (p.name)}
					{@const b = driftBadge(p)}
					<!-- svelte-ignore a11y_no_redundant_roles -->
					<tr role="row" data-state={p.state}>
						<td role="cell" class="mono" data-label="Parameter" title={p.desc ?? ''}>{p.name}</td>
						<td role="cell" class="num" class:oor={p.outOfRange} data-label="Plant">
							{valueText(p.value)}{p.unit ? ` ${p.unit}` : ''}{#if p.outOfRange}<span class="flag" title="outside min/max"> out of range</span>{/if}
						</td>
						<td role="cell" class="num" data-label="Repo init">{valueText(p.init)}{p.unit ? ` ${p.unit}` : ''}</td>
						<td role="cell" class="num" class:none={!rangeText(p)} data-label="Range">{rangeText(p)}</td>
						<td role="cell" data-label="State" title={b.hint}><StatusPill kind={b.kind} label={b.label} /></td>
						<td role="cell" class="detail" class:none={!who(p)} data-label="Last edit">{who(p)}</td>
						<td role="cell" class="act" class:none={!canTake(p)} data-label="">
							{#if canTake(p)}
								<Button size="sm" variant="secondary" aria-label={`Use repo value for ${p.name}`} disabled={busy} onclick={() => doTake([p])}>Use repo value</Button>
							{/if}
						</td>
					</tr>
				{:else}
					<tr role="row" class="empty">
						<td role="cell" colspan="7">
							{loading ? 'Reading…' : rows.length && driftOnly ? 'Every parameter is in step.' : 'No parameters.'}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</div>

<style>
	.wrap {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 10px;
	}
	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		font-size: var(--font-xs);
		color: var(--ink-2);
	}
	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}
	.grow {
		flex: 1 1 0;
	}
	.only {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.wb {
		color: var(--muted);
		font-size: var(--font-2xs);
		font-family: var(--mono);
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
	td.oor {
		color: var(--crit);
	}
	.flag {
		font-family: inherit;
		font-size: var(--font-2xs);
	}
	td.detail {
		color: var(--muted);
		font-size: var(--font-2xs);
		white-space: normal;
	}
	tr.empty td {
		text-align: center;
		color: var(--muted);
		padding: 16px;
	}
	.msg:empty {
		display: none;
	}
	.msg {
		margin: 0;
		font-size: var(--font-xs);
		color: var(--muted);
	}
	.msg.err {
		color: var(--crit);
	}
	@container (max-width: 640px) {
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
</style>
