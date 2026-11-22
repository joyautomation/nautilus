<script lang="ts">
	// One page for one piece of equipment (#714): live values with a trend of
	// the chosen ones, what an operator may set or command (greyed by role),
	// the equipment's alarms and their history, its notes, the audit of what
	// was written to it, and a camera when the descriptor has one. Everything
	// is the kit's own part, composed from an `AssetDescriptor`; the page
	// draws nothing the descriptor did not ask for.
	//
	//   <AssetPage {asset} {tags} {write} alarms={alarms} />
	import { untrack } from 'svelte';
	import type { AlarmClient, AlarmEvent } from '../alarms.svelte.js';
	import { STATE_META, PRIORITY_META } from '../alarms.svelte.js';
	import { assetAlarms, assetTitle, hasTag, tagValue, trendTags, writableTags, type AssetDescriptor } from '../asset.js';
	import type { MimicPanelReading } from '../mimic.js';
	import { sampleTag, tagHistory } from '../taghistory.svelte.js';
	import { deniedFor } from '../auth.svelte.js';
	import { AuditClient, type AuditFilter } from '../audit.js';
	import { NotesClient } from '../notes.js';
	import StatusPill from './StatusPill.svelte';
	import Sparkline from './Sparkline.svelte';
	import TrendChart from './Trend.svelte';
	import SetpointGroup from './SetpointGroup.svelte';
	import CommandButton from './CommandButton.svelte';
	import WriteToggle from './WriteToggle.svelte';
	import AlarmJournal from './AlarmJournal.svelte';
	import AuditTable from './AuditTable.svelte';
	import NotesPanel from './NotesPanel.svelte';
	import OutOfServiceBadge from './OutOfServiceBadge.svelte';

	let {
		asset,
		tags = {},
		write,
		alarms = null,
		auditClient = new AuditClient(),
		notesClient = new NotesClient()
	}: {
		asset: AssetDescriptor;
		/** The live tag values, by name (the frame's tags). */
		tags?: Record<string, unknown>;
		write?: (name: string, value: unknown) => Promise<string | null>;
		/** The alarm client: its instances give the alarms, its journal the history. Null: no alarm sections. */
		alarms?: AlarmClient | null;
		auditClient?: AuditClient;
		notesClient?: NotesClient;
	} = $props();

	const p = $derived(asset.panel);
	const value = (tag: string): unknown => tagValue(tags, tag);
	const noWrite = async () => 'This screen is read-only';
	const shown = (r: MimicPanelReading): string => {
		const v = value(r.tag);
		if (typeof v === 'number' && isFinite(v)) return v.toFixed(r.decimals ?? 1);
		if (typeof v === 'string' && v) return v;
		return '—';
	};

	// History from the moment the page opens, for the readings and the trend.
	const trended = $derived(trendTags(asset));
	$effect(() => {
		const names = [...new Set([...(p.readings ?? []).map((r) => r.tag), ...trended])];
		const id = setInterval(() => {
			for (const n of names) sampleTag(n, value(n));
		}, 1000);
		return () => clearInterval(id);
	});
	const COLORS = ['var(--accent, #4aa3ff)', 'var(--good, #4cc38a)', 'var(--serious, #ec835a)', 'var(--ink-2, #c3c2b7)'];
	const series = $derived(
		trended.map((tag, i) => {
			const h = tagHistory(tag);
			const now = Date.now();
			return { name: p.readings?.find((r) => r.tag === tag)?.label ?? tag, color: COLORS[i % COLORS.length], points: h.map((v, k) => ({ t: now - (h.length - 1 - k) * 1000, v })) };
		})
	);

	// What the role may write: a convenience; the controller decides.
	const setpoints = $derived((p.setpoints ?? []).filter((s) => !deniedFor('write', s.tag)));
	const lockedSetpoints = $derived((p.setpoints ?? []).length - setpoints.length);

	// Alarms of this equipment, and their history.
	const mine = $derived(alarms ? assetAlarms(alarms.instances, asset) : []);
	let to = $state(Date.now());
	let from = $state(Date.now() - 24 * 3600 * 1000);
	let events = $state<AlarmEvent[]>([]);
	let loading = $state(false);
	// Reads land in order: a slower, older answer never replaces a newer one.
	let seq = 0;
	async function loadHistory(f: number, t: number) {
		if (!alarms) return;
		const mine = ++seq;
		const ids = assetAlarms(alarms.instances, asset).map((a) => a.id);
		from = f;
		to = t;
		if (!ids.length) {
			events = [];
			loading = false;
			return;
		}
		loading = true;
		try {
			const got = (await alarms.journal(f, t, { ids })).events;
			if (mine === seq) events = got;
		} finally {
			if (mine === seq) loading = false;
		}
	}
	// A rolling window (the last day, as picked) follows the clock and reloads
	// when an alarm of this equipment appears or the alarm rev moves; a range
	// the person chose stays where it is.
	let custom = $state(false);
	const idsKey = $derived(mine.map((a) => a.id).join(','));
	const rev = $derived(alarms?.summary?.rev ?? 0);
	$effect(() => {
		void idsKey;
		void rev;
		// only these re-run it: the load writes the range it reads
		untrack(() => {
			if (custom) void loadHistory(from, to);
			else void loadHistory(Date.now() - (to - from), Date.now());
		});
	});
	function pickRange(f: number, t: number) {
		custom = true;
		void loadHistory(f, t);
	}

	// Audit: the writes to one of its tags at a time (the controller filters on one target).
	const writable = $derived(writableTags(asset));
	let auditPick = $state('');
	const auditTag = $derived(writable.includes(auditPick) ? auditPick : (writable[0] ?? ''));
	const auditFilter = $derived<AuditFilter>({ target: auditTag, limit: 50 });
</script>

<article class="asset" aria-label={assetTitle(asset)}>
	<header>
		<h1>{assetTitle(asset)}</h1>
		{#if asset.subtitle}<p class="sub">{asset.subtitle}</p>{/if}
		{#if p.text}<p class="sub">{p.text}</p>{/if}
	</header>

	<div class="cols">
		<section aria-label="Live values">
			<h2>Live values</h2>
			{#if p.readings?.length}
				<dl class="readings">
					{#each p.readings as r, i (i)}
						{@const v = value(r.tag)}
						<dt>{r.label ?? r.tag}</dt>
						<dd>
							{#if typeof v === 'boolean'}
								<StatusPill
									kind={v ? (r.alarm ? 'critical' : 'good') : 'off'}
									label={v ? (r.on ?? (r.alarm ? 'Alarm' : 'On')) : (r.off ?? (r.alarm ? 'Normal' : 'Off'))}
								/>
							{:else}
								<span class="num">{shown(r)}</span>{#if r.unit}<span class="unit">{r.unit}</span>{/if}
								{#if typeof v === 'number'}
									<span class="spark" aria-hidden="true">
										<Sparkline values={tagHistory(r.tag)} height={18} yMin={r.min} yMax={r.max} color="var(--ink-2, #c3c2b7)" compact />
									</span>
								{/if}
							{/if}
						</dd>
					{/each}
				</dl>
			{:else}
				<p class="none">No readings.</p>
			{/if}
			{#if series.length}
				<TrendChart {series} height={180} />
			{/if}
		</section>

		{#if p.commands?.length || p.toggles?.length || p.setpoints?.length}
			<section aria-label="Settings and commands">
				<h2>Settings and commands</h2>
				{#if p.commands?.length}
					<div class="row">
						{#each p.commands as c, i (i)}
							{@const denied = deniedFor('write', c.tag)}
							<CommandButton
								tag={c.tag}
								label={c.label}
								kind={c.kind ?? 'neutral'}
								pulseMs={c.pulseMs ?? 400}
								held={c.held ? value(c.held) === true : false}
								disabled={!!denied}
								disabledReason={denied}
								write={write ?? noWrite}
							/>
						{/each}
					</div>
				{/if}
				{#if p.toggles?.length}
					<div class="row">
						{#each p.toggles as t, i (i)}
							{@const denied = deniedFor('write', t.tag)}
							<WriteToggle
								tag={t.tag}
								label={t.label}
								value={value(t.tag) === true}
								present={hasTag(tags, t.tag)}
								invert={t.invert ?? false}
								readonly={!!denied}
								readonlyReason={denied}
								write={write ?? noWrite}
							/>
						{/each}
					</div>
				{/if}
				{#if setpoints.length}
					<SetpointGroup title="Setpoints" items={setpoints} {tags} {write} />
				{/if}
				{#if lockedSetpoints}
					<p class="none">{lockedSetpoints} setpoint{lockedSetpoints === 1 ? '' : 's'} hidden: your role may not change {lockedSetpoints === 1 ? 'it' : 'them'}.</p>
				{/if}
			</section>
		{/if}
	</div>

	{#if asset.camera}
		<section aria-label="Camera">
			<h2>{asset.camera.label ?? 'Camera'}</h2>
			<img class="camera" src={asset.camera.url} alt={asset.camera.label ?? `${assetTitle(asset)} camera`} />
		</section>
	{/if}

	{#if alarms}
		<section aria-label="Alarms">
			<h2>Alarms</h2>
			{#if mine.length}
				<ul class="alarms">
					{#each mine as a (a.id)}
						{@const meta = STATE_META[a.state]}
						<li>
							<span class="state" style="--c: {meta.color}"><span class="dot" aria-hidden="true"></span>{meta.label}</span>
							<span class="name">{a.name}</span>
							<span class="prio">{PRIORITY_META[a.priority]?.label ?? a.priority}</span>
							{#if a.state === 'suppressed'}<OutOfServiceBadge alarm={a} />{/if}
						</li>
					{/each}
				</ul>
			{:else}
				<p class="none">No alarm is standing on this equipment.</p>
			{/if}
		</section>
		<section aria-label="Alarm history">
			<h2>Alarm history</h2>
			<AlarmJournal {events} {from} {to} {loading} onrange={pickRange} />
		</section>
	{/if}

	{#if asset.notes !== false}
		<section aria-label="Notes">
			<NotesPanel client={notesClient} target={{ kind: 'asset', id: asset.id }} heading="Notes" />
		</section>
	{/if}

	{#if writable.length}
		<section aria-label="Audit">
			<h2>Audit</h2>
			<label class="pick">
				<span>Writes to</span>
				<select value={auditTag} onchange={(e) => (auditPick = e.currentTarget.value)}>
					{#each writable as w (w)}<option value={w}>{w}</option>{/each}
				</select>
			</label>
			{#key auditFilter.target}
				<AuditTable client={auditClient} filter={auditFilter} limit={50} />
			{/key}
		</section>
	{/if}
</article>

<style>
	.asset {
		display: grid;
		gap: var(--space-4, 16px);
		max-width: 1200px;
		margin: 0 auto;
	}
	header h1 {
		margin: 0;
		font-size: var(--font-lg);
	}
	.sub,
	.none {
		margin: 0;
		color: var(--muted);
		font-size: var(--font-xs);
	}
	.cols {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
		gap: var(--space-4, 16px);
		align-items: start;
	}
	section {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		padding: var(--space-3, 12px);
		display: grid;
		gap: var(--space-2, 8px);
		min-width: 0;
	}
	h2 {
		margin: 0;
		font-size: var(--font-md, 1rem);
	}
	.readings {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-1) var(--space-3);
		margin: 0;
		font-size: var(--font-sm, 0.9rem);
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}
	.num {
		font-variant-numeric: tabular-nums;
		font-weight: 600;
	}
	.unit {
		color: var(--muted);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
	.alarms {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: var(--space-1);
		font-size: var(--font-xs);
	}
	.alarms li {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		align-items: baseline;
	}
	.state {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		color: var(--c);
		font-weight: 600;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--c);
	}
	.prio {
		color: var(--muted);
	}
	.camera {
		max-width: 100%;
		border-radius: var(--radius-sm);
	}
	.pick {
		display: flex;
		gap: var(--space-2);
		align-items: center;
		font-size: var(--font-xs);
	}
	.pick select {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--ink);
		font: inherit;
		padding: var(--space-1) var(--space-2);
	}
</style>
