<script lang="ts">
	// The drivers and devices of a controller, as forms (#711, slice 2).
	// It reads the manifest and the schema from the controller, edits the
	// manifest text where it changed (../config/yamlpatch.ts) and reports
	// the overlay of changed files through `onoverlay`: the {files} the
	// staged-apply routes take. It stages and applies nothing itself.
	import { untrack } from 'svelte';
	import { ConfigClient, type ConfigFiles } from '../config/client.js';
	import { sectionField, validate, type Field, type Json, type Issue, type Schema } from '../config/schema.js';
	import { driverRows, deviceRows, setField, addDriver, addDevice, removeRow, type Overlay, type Row, type RowRef } from '../config/form.js';
	import { patch, type Op } from '../config/yamlpatch.js';
	import SchemaForm from './SchemaForm.svelte';
	import ModbusPoints from './ModbusPoints.svelte';
	import { modbusManifests } from '../config/modbus.js';
	import Button from './Button.svelte';
	import ConfigReview from './ConfigReview.svelte';
	import { ReviewClient } from '../config/review.js';

	let {
		client = new ConfigClient(),
		review = new ReviewClient(),
		onoverlay
	}: {
		client?: ConfigClient;
		/** The staged-apply client the review panel drives. */
		review?: ReviewClient;
		/** Called with the files changed so far (empty when nothing is). */
		onoverlay?: (o: Overlay) => void;
	} = $props();

	let loaded = $state<ConfigFiles | null>(null);
	let fields = $state<{ drivers: Field; devices: Field } | null>(null);
	let texts = $state<Record<string, string>>({});
	let error = $state<string | null>(null);
	let overlay = $state<Overlay>({ files: {}, changed: [] });

	const manifestPath = $derived(loaded?.manifest ?? 'nautilus.yaml');
	const manifest = $derived(texts[manifestPath] ?? '');
	const drivers = $derived.by(() => {
		try {
			return driverRows(manifest);
		} catch (e) {
			return e as Error;
		}
	});
	const devices = $derived.by(() => {
		try {
			return manifest ? deviceRows(manifest) : [];
		} catch {
			return [];
		}
	});
	const modbus = $derived(loaded ? modbusManifests(texts, manifestPath) : []);
	const edited = $derived(!!loaded && Object.keys(texts).some((p) => texts[p] !== loaded?.files[p]));

	async function load() {
		const f = await client.files();
		if (!f.ok) return void (error = f.message);
		const [d1, d2, dv] = await Promise.all([client.schema('drivers'), client.schema('driver'), client.schema('devices')]);
		for (const r of [d1, d2, dv]) if (!r.ok) return void (error = r.message);
		const s = (r: typeof d1) => (r.ok ? (r.value as Schema) : {});
		error = null;
		loaded = f.value;
		texts = { ...f.value.files };
		overlay = { files: {}, changed: [] };
		onoverlay?.(overlay);
		let driversField: Field;
		try {
			const single = driverRows(f.value.files[f.value.manifest] ?? '')[0]?.path[0] === 'driver';
			driversField = single ? sectionField(s(d2), 'driver') : (sectionField(s(d1), 'drivers').item as Field);
		} catch {
			driversField = sectionField(s(d1), 'drivers').item as Field;
		}
		fields = { drivers: driversField, devices: sectionField(s(dv), 'devices').item as Field };
	}
	$effect(() => {
		untrack(() => void load());
	});

	function report() {
		if (!loaded) return;
		const changed: Record<string, string> = {};
		for (const [p, t] of Object.entries(texts)) if (t !== loaded.files[p]) changed[p] = t;
		overlay = { files: changed, changed: Object.keys(changed).sort() };
		onoverlay?.(overlay);
	}

	function run(op: () => Op, file = manifestPath) {
		try {
			texts[file] = patch(texts[file] ?? '', [op()]);
			error = null;
			report();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	const issuesOf = (f: Field, r: Row): Issue[] => validate({ ...f, required: true }, r ?? {}, [f.key]);
	const label = (r: RowRef) => String(r.value?.name ?? r.value?.type ?? r.path.join('.'));
</script>

<section class="config">
	{#if error}<p class="msg" role="alert">{error}</p>{/if}
	{#if loaded && fields}
		{#if loaded.differs?.length}
			<p class="note">Changes applied live and not yet committed: {loaded.differs.join(', ')}.</p>
		{/if}
		<h3>Drivers</h3>
		{#if drivers instanceof Error}
			<p class="msg">{drivers.message}</p>
		{:else}
			{#each drivers as r (r.path.join('.'))}
				<article>
					<header>
						<strong>{label(r)}</strong>
						<Button size="sm" variant="ghost" onclick={() => run(() => removeRow(manifest, r))}>Remove</Button>
					</header>
					<SchemaForm field={fields.drivers} value={r.value} issues={issuesOf(fields.drivers, r.value)} onedit={(p, v) => run(() => setField(r.path, p[p.length - 1], v))} />
				</article>
			{/each}
			<Button size="sm" onclick={() => run(() => addDriver(manifest, { type: 'memory' }))}>Add a driver</Button>
		{/if}
		<h3>Devices</h3>
		{#each devices as r (r.path.join('.'))}
			<article>
				<header>
					<strong>{label(r)}</strong>
					<Button size="sm" variant="ghost" onclick={() => run(() => removeRow(manifest, r))}>Remove</Button>
				</header>
				<SchemaForm field={fields.devices} value={r.value} issues={issuesOf(fields.devices, r.value)} onedit={(p, v) => run(() => setField(r.path, p[p.length - 1], v))} />
			</article>
		{/each}
		<Button size="sm" onclick={() => run(() => addDevice({ name: 'NewDevice', profile: '', endpoint: '' }))}>Add a device</Button>
		{#each modbus as mm (mm.path)}
			<article><ModbusPoints path={mm.path} driver={mm.driver} classes={mm.classes} text={texts[mm.path] ?? ''} onedit={(p, op) => run(op, p)} /></article>
		{/each}
		<p class="note">{edited ? 'Edited, not applied.' : 'No changes.'}</p>
		<ConfigReview before={loaded.files} {overlay} client={review} onapplied={() => void load()} />
	{:else if !error}
		<p class="note">Reading the configuration…</p>
	{/if}
</section>

<style>
	.config { display: grid; gap: var(--space-3); font-size: var(--font-sm); }
	h3 { margin: var(--space-3) 0 0; font-size: var(--font-md); }
	article { display: grid; gap: var(--space-2); padding: var(--space-3); border: 1px solid color-mix(in srgb, currentColor 15%, transparent); border-radius: var(--radius); }
	header { display: flex; justify-content: space-between; align-items: center; }
	.note { margin: 0; color: var(--muted); font-size: var(--font-xs); }
	.msg { margin: 0; padding: var(--space-2) var(--space-3); border: 1px solid var(--crit); color: var(--crit); border-radius: var(--radius-sm); }
</style>
