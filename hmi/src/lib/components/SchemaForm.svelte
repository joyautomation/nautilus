<script lang="ts">
	// One manifest object as a form, from a field tree built from the
	// controller's schema slice (#711; ../config/schema.ts). It edits
	// nothing itself: each change is reported as (path, value) and the
	// owner turns it into a text patch. Settings the form does not read
	// (`raw`) are shown as their YAML text and left alone.
	import type { Field, Json, Issue } from '../config/schema.js';
	import { stringify } from 'yaml';
	import SchemaForm from './SchemaForm.svelte';

	let {
		field,
		value,
		path = [],
		issues = [],
		readonly = false,
		onedit
	}: {
		/** An object field: its properties are the rows of the form. */
		field: Field;
		value: { [k: string]: Json } | undefined;
		path?: string[];
		issues?: Issue[];
		readonly?: boolean;
		/** value undefined clears the setting. */
		onedit: (path: string[], value: Json | undefined) => void;
	} = $props();

	const here = (k: string) => path.concat(k).join('.');
	const problems = (k: string) => issues.filter((i) => i.path.endsWith(here(k)) || i.path.endsWith(`${field.key}.${k}`));

	function text(f: Field, v: Json | undefined): string {
		if (v === undefined || v === null) return '';
		if (f.kind === 'list' && f.item && ['string', 'enum', 'integer', 'number'].includes(f.item.kind) && Array.isArray(v)) return v.join(', ');
		if (typeof v === 'object') return JSON.stringify(v);
		return String(v);
	}

	function commit(f: Field, raw: string) {
		const t = raw.trim();
		if (t === '') return onedit(path.concat(f.key), undefined);
		switch (f.kind) {
			case 'integer':
			case 'number': {
				const n = Number(t);
				return onedit(path.concat(f.key), Number.isFinite(n) ? n : t); // a non-number is the validator's to name
			}
			case 'list':
				if (f.item && ['string', 'enum'].includes(f.item.kind)) return onedit(path.concat(f.key), t.split(',').map((s) => s.trim()).filter(Boolean));
				if (f.item && ['integer', 'number'].includes(f.item.kind)) return onedit(path.concat(f.key), t.split(',').map((s) => Number(s.trim())));
			// fallthrough: a list of objects is JSON text
			case 'map':
				try {
					return onedit(path.concat(f.key), JSON.parse(t) as Json);
				} catch {
					return; // not JSON yet: keep what the person is typing
				}
			default:
				return onedit(path.concat(f.key), t);
		}
	}
</script>

<div class="form">
	{#each field.fields ?? [] as f (f.key)}
		{@const v = value?.[f.key]}
		{@const bad = problems(f.key)}
		<label class="row" class:invalid={bad.length > 0}>
			<span class="k">{f.key}{#if f.required}<abbr title="required">*</abbr>{/if}</span>
			{#if f.kind === 'raw'}
				<pre class="raw" title={f.rawWhy}>{v === undefined ? '' : stringify(v).trimEnd()}</pre>
			{:else if f.kind === 'boolean'}
				<select disabled={readonly} value={v === undefined ? '' : String(v)} onchange={(e) => onedit(path.concat(f.key), e.currentTarget.value === '' ? undefined : e.currentTarget.value === 'true')}>
					<option value="">(default)</option>
					<option value="true">true</option>
					<option value="false">false</option>
				</select>
			{:else if f.kind === 'enum'}
				<select disabled={readonly} value={typeof v === 'string' ? v : ''} onchange={(e) => onedit(path.concat(f.key), e.currentTarget.value === '' ? undefined : e.currentTarget.value)}>
					{#if !f.required}<option value="">(default)</option>{/if}
					{#each f.enum ?? [] as o}<option value={o}>{o}</option>{/each}
				</select>
			{:else}
				<input
					disabled={readonly}
					autocomplete="off"
					spellcheck="false"
					value={text(f, v)}
					placeholder={f.kind === 'list' ? 'a, b, c' : f.kind === 'map' ? '{"key": "value"}' : ''}
					inputmode={f.kind === 'integer' || f.kind === 'number' ? 'decimal' : undefined}
					onchange={(e) => commit(f, e.currentTarget.value)}
				/>
			{/if}
			{#if f.description}<span class="d">{f.description.split(/\.\s/)[0]}</span>{/if}
			{#each bad as i}<span class="e">{i.message}</span>{/each}
		</label>
	{/each}
</div>

<style>
	.form { display: grid; gap: var(--space-2); font-size: var(--font-sm); }
	.row { display: grid; grid-template-columns: 11rem minmax(0, 1fr); gap: var(--space-1) var(--space-3); align-items: center; }
	.k { font-family: var(--mono); font-size: var(--font-xs); }
	abbr { color: var(--crit); text-decoration: none; margin-left: 2px; }
	input, select { font: inherit; padding: var(--space-1) var(--space-2); border-radius: var(--radius-sm); min-width: 0; }
	.invalid input, .invalid select { outline: 1px solid var(--crit); }
	.d { grid-column: 2; color: var(--muted); font-size: var(--font-2xs); }
	.e { grid-column: 2; color: var(--crit); font-size: var(--font-xs); }
	.raw { margin: 0; font-family: var(--mono); font-size: var(--font-xs); color: var(--muted); white-space: pre-wrap; }
</style>
