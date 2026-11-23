<script lang="ts">
	// The I/O checkout (#719): the sheet's points, checked off by a person at
	// the field device. Observed points are marked Pass, Fail or Note; the
	// controller writes the reading and quality it saw into the result, never
	// this page. An output the sheet says to force can be driven for a few
	// seconds once its equipment is out of service: typed confirmation, a fresh
	// password (the kit's fetch asks), and the controller keeps the clock. A
	// rehearsal run is marked as one and can never be signed.
	import { onMount } from 'svelte';
	import {
		CheckoutClient,
		FORCE_SECONDS,
		canIn,
		clock,
		forceValue,
		needsNote,
		offersForce,
		offersSeal,
		runLinks,
		SEAL_NOTICE,
		resultLine,
		countdown,
		type CheckoutForce,
		type CheckoutPoint,
		type CheckoutRunView,
		type CheckoutView,
		type Progress
	} from '../checkout.js';
	import { currentAuth } from '../auth.svelte.js';
	import Button from './Button.svelte';
	import Modal from './Modal.svelte';

	let { force = null, client = new CheckoutClient() }: { force?: CheckoutForce | null; client?: CheckoutClient } = $props();

	let view = $state<CheckoutView | null>(null);
	let run = $state<CheckoutRunView | null>(null);
	let message = $state('');
	let off = $state('');
	let busy = $state(false);
	let noteFor = $state<{ id: string; outcome: 'fail' | 'note' } | null>(null);
	let noteText = $state('');
	let noteProblem = $state('');

	const me = $derived(currentAuth()?.me ?? null);
	const mayCheck = (pt: CheckoutPoint) => canIn(me, 'checkout', pt.area);

	async function load() {
		const a = await client.view();
		if (!a.ok) {
			// 404: this project has no checkout (the page says so rather than showing an empty sheet)
			if (a.status === 404 || a.status === 503) off = a.message;
			else message = a.message;
			return;
		}
		off = '';
		view = a.value;
		if (a.value.run) {
			const r = await client.run(a.value.run.id);
			if (r.ok) run = r.value;
			else message = r.message;
		} else run = null;
	}
	onMount(() => void load());

	// The answer's own words come back with it, so a dialog can show them (the
	// page behind an open dialog cannot be read) and an unknown outcome keeps
	// its "may have been recorded" text instead of becoming "did not work".
	async function act(p: Promise<{ ok: boolean; message?: string }>): Promise<{ ok: boolean; message: string }> {
		busy = true;
		message = '';
		try {
			const a = await p;
			const said = a.ok ? '' : (a.message ?? 'did not work');
			message = said;
			await load();
			return { ok: a.ok, message: said };
		} finally {
			busy = false;
		}
	}

	const begin = (rehearsal: boolean) => act(client.begin(rehearsal));
	// abandoning says why, in a dialog of the page's own (a native prompt can be
	// blocked or hidden, and then nothing happens)
	let abandoning = $state(false);
	let abandonWhy = $state('');
	let abandonProblem = $state('');
	const abandon = () => {
		abandonWhy = '';
		abandonProblem = '';
		abandoning = true;
	};
	async function doAbandon() {
		if (!run) return;
		if (!abandonWhy.trim()) {
			abandonProblem = 'say why: an abandoned run keeps its results and its reason';
			return;
		}
		const a = await act(client.abandon(run.id, abandonWhy.trim()));
		if (a.ok) abandoning = false;
		else abandonProblem = a.message;
	}

	function mark(pt: CheckoutPoint, outcome: 'pass' | 'fail' | 'note') {
		if (!run) return;
		if (needsNote(outcome)) {
			noteFor = { id: pt.id, outcome: outcome as 'fail' | 'note' };
			noteText = '';
			noteProblem = '';
			return;
		}
		void act(client.record(run.id, pt.id, outcome));
	}
	async function saveNote() {
		if (!run || !noteFor) return;
		if (!noteText.trim()) {
			noteProblem = 'say why: a fail or a note needs words';
			return;
		}
		const a = await act(client.record(run.id, noteFor.id, noteFor.outcome, noteText.trim()));
		if (a.ok) noteFor = null;
		else noteProblem = a.message;
	}

	// --- force ---
	let forcing = $state<CheckoutPoint | null>(null);
	let valueText = $state('');
	let seconds = $state(30);
	let typed = $state('');
	let problem = $state('');

	function beginForce(pt: CheckoutPoint) {
		forcing = pt;
		valueText = '';
		seconds = FORCE_SECONDS[1].seconds;
		typed = '';
		problem = '';
	}
	async function doForce() {
		if (!run || !forcing) return;
		const v = forceValue(forcing.kind, valueText);
		if (!v.ok) {
			problem = v.error;
			return;
		}
		if (typed !== forcing.id) {
			problem = `type ${forcing.id} to confirm`;
			return;
		}
		const a = await act(client.force(run.id, forcing.id, v.value, seconds, typed));
		if (a.ok) forcing = null;
		else problem = a.message;
	}
	const release = () => run && act(client.release(run.id));

	// --- seal (sign-off) ---
	let sealing = $state(false);
	let sealNote = $state('');
	let sealProblem = $state('');
	async function doSeal() {
		if (!run) return;
		sealProblem = '';
		const a = await act(client.seal(run.id, sealNote.trim()));
		if (a.ok) sealing = false;
		else sealProblem = a.message;
	}
	const pastRuns = $derived((view?.runs ?? []).slice().reverse());
	const fmtAt = (ms?: number) => (ms ? new Date(ms).toLocaleString() : '');

	let now = $state(0);
	let frameAt = $state(0);
	$effect(() => {
		force;
		frameAt = now = performance.now();
	});
	onMount(() => {
		const id = setInterval(() => (now = performance.now()), 500);
		return () => clearInterval(id);
	});
	const standing = $derived(countdown(force, frameAt, now));
	const heldBy = (pt: CheckoutPoint) => !!force && !!run && force.run === run.id && force.point === pt.id && standing.state !== 'none';

	const pct = (p: Progress) => (p.total ? Math.round((100 * p.pass) / p.total) : 0);
	const entries = (m: Record<string, Progress> | undefined) => Object.entries(m ?? {}).sort(([a], [b]) => a.localeCompare(b));
</script>

<section class="ck">
	{#if off}
		<p class="none">{off}</p>
	{:else if !view}
		<p class="none">Reading the checkout…</p>
	{:else}
		<header>
			<h2>{view.title || 'I/O checkout'}</h2>
			<span class="meta">sheet {view.hash.slice(0, 8)}</span>
			{#if run}
				<span class="badge" class:rehearsal={run.rehearsal}>{run.rehearsal ? 'Rehearsal: can never be signed' : 'Live run'}</span>
				<a class="exp" href={`/api/checkout/runs/${encodeURIComponent(run.id)}/export?format=csv`} download>CSV</a>
				<a class="exp" href={`/api/checkout/runs/${encodeURIComponent(run.id)}/export?format=json`} download>JSON</a>
				<Button size="sm" variant="ghost" disabled={busy} onclick={abandon}>Abandon run</Button>
				{#if offersSeal(me, run)}
					<Button size="sm" variant="primary" disabled={busy || !!force} title={force ? 'let the force go first' : undefined} onclick={() => { sealing = true; sealNote = ''; sealProblem = ''; }}>Sign off…</Button>
				{/if}
			{:else}
				<Button size="sm" disabled={busy} onclick={() => begin(false)}>Begin run</Button>
				<Button size="sm" variant="secondary" disabled={busy} onclick={() => begin(true)}>Begin rehearsal</Button>
			{/if}
		</header>

		{#if run?.unreal}<p class="problem" role="status">This run can never be signed off: {run.unreal}</p>{/if}
		{#if view.keyFingerprint}<p class="meta">Signing key {view.keyFingerprint}: write it down from here, ahead of time, to check a sealed record against.</p>{/if}
		{#if message}<p class="problem" role="alert">{message}</p>{/if}
		{#if view.sheetChanged}
			<p class="problem" role="alert">The sheet changed since this run began: abandon it and begin again, so every result is against one sheet.</p>
		{/if}

		{#if run}
			<div class="progress">
				{#each [{ label: 'Area', rows: entries(run.byArea) }, { label: 'Device', rows: entries(run.byDevice) }] as g (g.label)}
					<table>
						<thead><tr><th>{g.label}</th><th>Pass</th><th>Fail</th><th>Note</th><th>To do</th><th></th></tr></thead>
						<tbody>
							{#each g.rows as [name, p] (name)}
								<tr>
									<td>{name || '(none)'}</td><td>{p.pass}</td><td>{p.fail}</td><td>{p.noted}</td><td>{p.pending}</td>
									<td><progress max="100" value={pct(p)}></progress></td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/each}
			</div>

			<div class="scroll"><table class="points">
				<thead><tr><th>Point</th><th>Tag</th><th>Kind</th><th>Where</th><th>Expect</th><th>Latest</th><th></th></tr></thead>
				<tbody>
					{#each view.points as pt (pt.id)}
						{@const r = run.latest[pt.id]}
						<tr>
							<td>{pt.id}</td>
							<td>{pt.tag}</td>
							<td>{pt.kind}</td>
							<td>{[pt.device, pt.address, pt.area].filter(Boolean).join(' · ')}</td>
							<td>{pt.expect ?? ''}</td>
							<td class:pass={r?.outcome === 'pass'} class:fail={r?.outcome === 'fail'}>{r ? resultLine(r) : 'not checked'}</td>
							<td class="acts">
								{#if mayCheck(pt) && run.state === 'open' && !view.sheetChanged}
									<Button size="sm" disabled={busy} onclick={() => mark(pt, 'pass')}>Pass</Button>
									<Button size="sm" variant="secondary" disabled={busy} onclick={() => mark(pt, 'fail')}>Fail</Button>
									<Button size="sm" variant="ghost" disabled={busy} onclick={() => mark(pt, 'note')}>Note</Button>
									{#if offersForce(pt) && canIn(me, 'force', pt.area)}
										{#if heldBy(pt)}
											<Button size="sm" variant="secondary" disabled={busy} onclick={release}>
												Release{standing.state === 'standing' ? ` (${clock(standing.remainingMs)})` : ''}
											</Button>
										{:else}
											<Button size="sm" variant="secondary" disabled={busy || !!force} title={force ? 'one checkout force at a time' : undefined} onclick={() => beginForce(pt)}>Force…</Button>
										{/if}
									{/if}
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table></div>
		{:else}
			<p class="none">No run is open. Begin one to check the sheet's {view.points.length} points.</p>
		{/if}
	{/if}
	{#if view && pastRuns.length}
		<h3>Runs</h3>
		<div class="scroll">
			<table>
				<thead><tr><th>Run</th><th>State</th><th>Begun</th><th>By</th><th>Results</th><th>Files</th></tr></thead>
				<tbody>
					{#each pastRuns as r (r.id)}
						<tr>
							<td>{r.id}{r.rehearsal ? ' (rehearsal)' : ''}</td>
							<td>{r.state === 'sealed' ? `sealed by ${r.sealedBy ?? ''} ${fmtAt(r.sealedAt)}` : r.state}</td>
							<td>{fmtAt(r.at)}</td><td>{r.by}</td><td>{r.results ?? ''}</td>
							<td class="acts">{#each runLinks(r) as l (l.label)}<a class="exp" href={l.href} download={l.label === 'Report' ? undefined : ''} target={l.label === 'Report' ? '_blank' : undefined} rel="noopener">{l.label}</a>{/each}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>

<Modal open={sealing} title="Sign off this run" size="sm" onclose={() => (sealing = false)}>
	{#snippet children()}
		{#if sealProblem}<p class="problem" role="alert">{sealProblem}</p>{/if}
		<p class="warn">{SEAL_NOTICE}</p>
		<p class="warn">The controller signs off only a live run on its current sheet with every point checked and none failing, and (by default) only for someone who recorded none of its results. Your password is asked next.</p>
		<label class="field"><span>Note (optional)</span><input type="text" bind:value={sealNote} maxlength="500" /></label>
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (sealing = false)}>Cancel</Button>
		<Button variant="primary" disabled={busy} onclick={doSeal}>Sign off</Button>
	{/snippet}
</Modal>

<Modal open={abandoning} title="Abandon this run" size="sm" onclose={() => (abandoning = false)}>
	{#snippet children()}
		{#if abandonProblem}<p class="problem" role="alert">{abandonProblem}</p>{/if}
		<p class="warn">An abandoned run takes no more results and can never be signed off. Its results stay, with your reason.</p>
		<label class="field"><span>Why (required)</span><input type="text" bind:value={abandonWhy} maxlength="200" /></label>
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (abandoning = false)}>Cancel</Button>
		<Button variant="primary" disabled={busy} onclick={doAbandon}>Abandon run</Button>
	{/snippet}
</Modal>

<Modal open={!!noteFor} title={noteFor ? `${noteFor.outcome === 'fail' ? 'Fail' : 'Note'}: ${noteFor.id}` : ''} size="sm" onclose={() => (noteFor = null)}>
	{#snippet children()}
		{#if noteProblem}<p class="problem" role="alert">{noteProblem}</p>{/if}
		<label class="field"><span>Why (required)</span><input type="text" bind:value={noteText} /></label>
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (noteFor = null)}>Cancel</Button>
		<Button variant="primary" disabled={busy} onclick={saveNote}>Record</Button>
	{/snippet}
</Modal>

<Modal open={!!forcing} title={forcing ? `Force ${forcing.id}` : ''} size="sm" onclose={() => (forcing = null)}>
	{#snippet children()}
		{#if forcing}
			{#if problem}<p class="problem" role="alert">{problem}</p>{/if}
			<p class="warn">
				This drives {forcing.tag} from the controller, bypassing the program's interlocks. Its equipment ({forcing.equipment || 'none named'}) must be out of service. The
				controller lets go of it when the time is up.
			</p>
			<label class="field">
				<span>{forcing.kind === 'DO' ? 'Value (on or off)' : 'Value (a number)'}</span>
				<input type="text" bind:value={valueText} />
			</label>
			<label class="field">
				<span>For</span>
				<select bind:value={seconds}>{#each FORCE_SECONDS as d (d.seconds)}<option value={d.seconds}>{d.label}</option>{/each}</select>
			</label>
			<label class="field"><span>Type {forcing.id} to confirm</span><input type="text" bind:value={typed} autocomplete="off" /></label>
			<p class="warn">Your password is asked next.</p>
		{/if}
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (forcing = null)}>Cancel</Button>
		<Button variant="primary" disabled={busy} onclick={doForce}>Force</Button>
	{/snippet}
</Modal>

<style>
	.ck { display: grid; gap: var(--space-2); }
	header { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
	h2 { margin: 0; font-size: var(--font-md, 1rem); }
	h3 { margin: var(--space-2) 0 0; font-size: var(--font-sm, 0.9rem); }
	.meta, .none { color: var(--muted); font-size: var(--font-xs); }
	.none { margin: 0; }
	.badge { font-size: var(--font-xs); font-weight: 700; padding: 2px 8px; border-radius: 999px; background: var(--surface-2); }
	.badge.rehearsal { background: color-mix(in srgb, var(--warn, #d98c00) 30%, var(--surface-2)); }
	.progress { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: var(--space-2); }
	table { width: 100%; border-collapse: collapse; font-size: var(--font-xs); }
	th, td { text-align: left; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border); }
	.exp { font-size: var(--font-xs); color: var(--ink-2, var(--muted)); }
	.scroll { overflow-x: auto; }
	.acts { white-space: nowrap; display: flex; gap: 4px; flex-wrap: wrap; }
	.pass { color: var(--good, #2a7); }
	.fail { color: var(--danger, #c33); }
	.field { display: grid; gap: var(--space-1); margin-bottom: var(--space-2); font-size: var(--font-xs); }
	.field select, .field input { background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius-sm); color: var(--ink); font: inherit; padding: var(--space-1) var(--space-2); }
	.warn { color: var(--muted); font-size: var(--font-xs); margin: 0 0 var(--space-2); }
	.problem { color: var(--danger, #c33); font-size: var(--font-xs); margin: 0; }
</style>
