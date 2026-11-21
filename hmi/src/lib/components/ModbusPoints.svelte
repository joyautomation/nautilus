<script lang="ts">
	// One Modbus manifest's sources and points as tables (#711, slice 5a).
	// Only what a live apply may change is editable: a source's host, port
	// and unit id, a point's table, address and scan class. The rest is
	// shown. It edits nothing itself: each change is reported as an op.
	import { parseModbus, problems, setSourceField, setTagField, TABLES, type SourceField, type TagField } from '../config/modbus.js';
	import type { Json } from '../config/schema.js';
	import type { Op } from '../config/yamlpatch.js';

	let {
		path,
		driver,
		classes = [],
		tagClasses = false,
		text,
		readonly = false,
		onedit
	}: {
		path: string;
		driver: string;
		/** The scan classes the driver declares (scan-classes:). */
		classes?: string[];
		/** The driver has tag-classes: patterns. */
		tagClasses?: boolean;
		text: string;
		readonly?: boolean;
		onedit: (path: string, op: () => Op) => void;
	} = $props();

	const m = $derived(parseModbus(text));
	const issues = $derived(problems(m, classes));
	let filter = $state('');
	const LIMIT = 200;
	const listUid = Math.random().toString(36).slice(2, 8);
	const matching = $derived(m.tags.filter((t) => !filter.trim() || t.name.toLowerCase().includes(filter.trim().toLowerCase()) || t.source.toLowerCase() === filter.trim().toLowerCase()));
	const shown = $derived(matching.slice(0, LIMIT));
	const bad = (where: string) => issues.filter((i) => i.where === where);

	function numberOr(raw: string): Json | undefined {
		const t = raw.trim();
		if (t === '') return undefined;
		const n = Number(t);
		return Number.isFinite(n) ? n : t; // not a number: the check names it
	}
	const srcEdit = (i: number, k: SourceField, raw: string) => onedit(path, () => setSourceField(i, k, k === 'host' ? raw.trim() : numberOr(raw)));
	const tagEdit = (i: number, k: TagField, raw: string) => onedit(path, () => setTagField(i, k, k === 'table' ? raw : k === 'address' ? numberOr(raw) : raw.trim() === '' ? undefined : raw.trim()));
</script>

<div class="points">
	<div class="head"><strong>Modbus points</strong> <code>{path}</code> <span class="d">driver {driver}</span></div>
	<p class="d">Table, address and scan class of a point, and a source's host, port and unit id, can change while running. A point's format, scale, direction, and adding or removing points, need a restart or a deploy. If this file is generated, re-running the import overwrites these edits.</p>
	{#if tagClasses}<p class="d">This driver has tag-classes: patterns, which override a point's scan class when it runs: the class shown here may not be the one a point polls at.</p>{/if}
	<table>
		<caption>Sources</caption>
		<thead><tr><th>Id</th><th>Host</th><th>Port</th><th>Unit id</th></tr></thead>
		<tbody>
			{#each m.sources as s (s.index)}
				<tr>
					<td>{s.id}</td>
					<td><input aria-label="source {s.id} host" disabled={readonly} value={s.host} class:invalid={bad('source ' + s.id).length > 0} onchange={(e) => srcEdit(s.index, 'host', e.currentTarget.value)} /></td>
					<td><input aria-label="source {s.id} port" class="n" disabled={readonly} value={s.port === undefined ? '' : String(s.port)} placeholder="502" inputmode="numeric" onchange={(e) => srcEdit(s.index, 'port', e.currentTarget.value)} /></td>
					<td><input aria-label="source {s.id} unit id" class="n" disabled={readonly} value={s.unitid === undefined ? '' : String(s.unitid)} placeholder="0" inputmode="numeric" onchange={(e) => srcEdit(s.index, 'unitid', e.currentTarget.value)} /></td>
				</tr>
			{/each}
		</tbody>
	</table>
	<label class="filter">Filter points <input type="search" bind:value={filter} placeholder="name or source id" /></label>
	<table>
		<caption>Points ({matching.length}{matching.length > LIMIT ? `, first ${LIMIT} shown` : ''})</caption>
		<thead><tr><th>Name</th><th>Source</th><th>Table</th><th>Address</th><th>Scan class</th><th>Format</th><th></th></tr></thead>
		<tbody>
			{#each shown as t (t.index)}
				<tr class:invalid={bad('tag ' + t.name).length > 0}>
					<td>{t.name}</td>
					<td>{t.source}</td>
					<td>
						<select aria-label="{t.name} table" disabled={readonly} value={t.table} onchange={(e) => tagEdit(t.index, 'table', e.currentTarget.value)}>
							{#if !(TABLES as readonly string[]).includes(t.table)}<option value={t.table}>{t.table}</option>{/if}
							{#each TABLES as o}<option value={o}>{o}</option>{/each}
						</select>
					</td>
					<td><input aria-label="{t.name} address" class="n" disabled={readonly} value={t.address ?? ''} inputmode="numeric" onchange={(e) => tagEdit(t.index, 'address', e.currentTarget.value)} /></td>
					<td><input aria-label="{t.name} scan class" disabled={readonly} value={t.scanclass ?? ''} placeholder="(default)" list={'sc-' + listUid} class:invalid={bad('tag ' + t.name).some((i) => i.message.includes('scan class'))} onchange={(e) => tagEdit(t.index, 'scanclass', e.currentTarget.value)} /></td>
					<td class="d">{t.format ?? (t.table === 'coil' || t.table === 'discrete' ? 'bool' : '(default)')}</td>
					<td class="d">{t.writable ? 'writable' : ''}</td>
				</tr>
			{/each}
		</tbody>
	</table>
	<datalist id={'sc-' + listUid}>{#each [...classes, 'none'] as c (c)}<option value={c}></option>{/each}</datalist>
	{#each issues as i}<p class="e">{i.message}</p>{/each}
</div>

<style>
	.points { display: grid; gap: var(--space-2); }
	.head { display: flex; gap: var(--space-3); align-items: baseline; }
	table { border-collapse: collapse; width: 100%; font-size: var(--font-sm); }
	caption { text-align: left; font-weight: 500; padding: var(--space-1) 0; }
	th { text-align: left; font-weight: 500; color: var(--muted); font-size: var(--font-xs); }
	td { padding: var(--space-1) var(--space-2) var(--space-1) 0; }
	input, select { font: inherit; padding: var(--space-1) var(--space-2); border-radius: var(--radius-sm); min-width: 0; width: 100%; }
	.n { width: 5rem; }
	.filter { display: flex; gap: var(--space-2); align-items: center; font-size: var(--font-xs); }
	.invalid input, .invalid select, input.invalid { outline: 1px solid var(--crit); }
	.d { color: var(--muted); font-size: var(--font-xs); margin: 0; }
	.e { color: var(--crit); font-size: var(--font-xs); margin: 0; }
</style>
