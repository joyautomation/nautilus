<script lang="ts">
	// Substitute values (#707): what is being held in place of a sensor, who
	// holds it, how long is left, and the controls to start, stop and
	// acknowledge. All the contract handling (requestId, unknown outcome, 409,
	// kept:false) lives in `SubstituteClient`; this component only renders its
	// answers — and never says "failed" about an outcome that is unknown.
	//
	// Pass a client you built (`new SubstituteClient({ token })`) or let it
	// make the default one. Wraps `confirm()` for the start and the stop, so a
	// `<ConfirmDialog/>` must be mounted once in the app.
	import {
		SubstituteClient,
		SubstituteSession,
		type PendingStart,
		failureText,
		formatRemaining,
		keptText,
		unacked,
		validateStart,
		maxSeconds,
		type SubstituteEntry,
		type SubstituteResult,
		type SubstituteRule,
		type SubstituteLapse
	} from '../substitutes.js';
	import { confirm } from '../confirm.svelte.js';
	import { deniedFor } from '../auth.svelte.js';
	import StatusPill from './StatusPill.svelte';
	import Button from './Button.svelte';

	let {
		client = new SubstituteClient(),
		pollMs = 2000,
		canStart = true
	}: {
		client?: SubstituteClient;
		/** Re-read interval; 0 reads on mount and after each action only. */
		pollMs?: number;
		/** False hides the start form (a read-only screen). */
		canStart?: boolean;
	} = $props();

	let subs = $state<SubstituteEntry[]>([]);
	let rules = $state<SubstituteRule[]>([]);
	let readError = $state<string | null>(null);
	let now = $state(Date.now());
	let readAt = $state(Date.now());

	// The start form.
	let tag = $state('');
	let valueText = $state('');
	let seconds = $state(60);
	let busy = $state(false);

	// What the last action said. `warn` is shown even on success (kept:false).
	let notice = $state<{ kind: 'ok' | 'warn' | 'error'; text: string } | null>(null);
	// A start whose outcome is unknown: kept WITH its requestId so a retry is
	// the same request, never a second substitute.
	const session = $derived(new SubstituteSession(client));
	let pending = $state<PendingStart[]>([]);
	let lapsed = $state<SubstituteLapse[]>([]);
	const blocked = $derived(pending.some((p) => p.name.toLowerCase() === tag.toLowerCase()));

	const rule = $derived(rules.find((r) => r.tag === tag));
	const waiting = $derived(unacked(subs));
	const startError = $derived(validateStart(rule, rule?.operatorValue ? parseValue(valueText) : undefined, seconds));

	function parseValue(t: string): unknown {
		const s = t.trim();
		if (s === '') return undefined;
		if (s === 'true') return true;
		if (s === 'false') return false;
		const n = Number(s);
		return Number.isFinite(n) ? n : s;
	}

	async function refresh() {
		const r = await client.list();
		if ('error' in r) {
			readError = r.error;
			return;
		}
		readError = null;
		subs = r.substitutes;
		rules = r.rules;
		lapsed = r.lapsed ?? [];
		readAt = Date.now();
		const gone = session.reconcile(r.substitutes, lapsed);
		if (gone.length) {
			notice = {
				kind: 'warn',
				text: `The start of ${gone.map((p) => p.name).join(', ')} lapsed: its outcome was unknown and it was committed too late, so it was NOT started. Start it again if it is still needed.`
			};
		}
		pending = session.pending();
		if (!tag && r.rules.length) tag = r.rules[0].tag;
	}

	$effect(() => {
		void refresh();
		const tick = setInterval(() => (now = Date.now()), 1000);
		const poll = pollMs > 0 ? setInterval(() => void refresh(), pollMs) : undefined;
		return () => {
			clearInterval(tick);
			if (poll) clearInterval(poll);
		};
	});

	// Time left, counted down locally between reads.
	function left(s: SubstituteEntry): string {
		return formatRemaining(s.remainingMs - (now - readAt));
	}

	async function settle(r: SubstituteResult, okText: string) {
		if (r.ok) {
			const w = keptText(r);
			notice = w ? { kind: 'warn', text: w } : { kind: 'ok', text: okText };
		} else {
			notice = { kind: r.kind === 'unknown' || r.kind === 'unreachable' || r.kind === 'pending' ? 'warn' : 'error', text: failureText(r) };
		}
		// Re-read after EVERY answer: for an unknown outcome this is the
		// operator's only way to see what actually happened.
		await refresh();
	}

	// What the signed-in role may substitute, by tag (#708). A convenience:
	// the controller decides.
	const startDenied = $derived(tag ? deniedFor('substitute', tag) : '');

	async function start() {
		const value = rule?.operatorValue ? parseValue(valueText) : undefined;
		if (startError) {
			notice = { kind: 'error', text: startError };
			return;
		}
		const ok = await confirm({
			title: `Substitute ${tag}?`,
			body: `The logic will run on ${value === undefined ? "the rule's value" : String(value)} instead of the sensor's reading for up to ${formatRemaining(seconds * 1000)}. It is not the plant's reading.`,
			confirmLabel: 'Substitute',
			danger: true
		});
		if (!ok) return;
		busy = true;
		const r = await session.start(tag, value, seconds);
		busy = false;
		pending = session.pending();
		await settle(r, `${tag} substituted.`);
	}

	async function retry(name: string) {
		busy = true;
		const r = await session.retry(name);
		busy = false;
		pending = session.pending();
		if (r) await settle(r, `${name} substituted.`);
	}

	async function stop(s: SubstituteEntry) {
		const ok = await confirm({
			title: `Stop substituting ${s.name}?`,
			body: 'The tag goes back to the sensor\'s own reading.',
			confirmLabel: 'Stop substitute'
		});
		if (!ok) return;
		busy = true;
		const r = await client.stop(s.id);
		busy = false;
		await settle(r, `${s.name} restored.`);
	}

	async function ack(ids: number[]) {
		busy = true;
		const r = await client.ack(ids);
		busy = false;
		await settle(r, 'Acknowledged.');
	}
</script>

<section class="subs" aria-label="Substitute values">
	{#if readError}
		<p class="msg error" role="alert">Cannot read substitutes: {readError}</p>
	{/if}

	{#if notice}
		<p class="msg {notice.kind}" role={notice.kind === 'ok' ? 'status' : 'alert'}>{notice.text}</p>
	{/if}

	{#each pending as p (p.requestId)}
		<div class="msg warn" role="alert">
			<span>
				The start of <b>{p.name}</b> is not confirmed — it may still take effect. Check the list, then retry (the same
				request, so it cannot start twice) or dismiss. A new start of this tag is blocked until then.
			</span>
			<span class="actions">
				<Button size="sm" disabled={busy} onclick={() => retry(p.name)}>Retry same request</Button>
				<Button size="sm" variant="ghost" onclick={() => { session.dismiss(p.name); pending = session.pending(); }}>Dismiss</Button>
			</span>
		</div>
	{/each}

	{#if subs.length}
		<table>
			<thead>
				<tr><th>Tag</th><th>Held</th><th>Live</th><th>By</th><th>Left</th><th></th></tr>
			</thead>
			<tbody>
				{#each subs as s (s.id)}
					<tr>
						<td class="name">{s.name}</td>
						<td class="num"><StatusPill kind="serious" label="SUBST" /> {String(s.value)}</td>
						<td class="num">{s.live === undefined ? '—' : String(s.live)} <span class="q">{s.liveQuality ?? ''}</span></td>
						<td>{s.by ?? '—'}</td>
						<td class="num">{left(s)}</td>
						<td class="act">
							{#if s.ackOnStart && !s.acked}<StatusPill kind="critical" label="Needs ack" />{/if}
							{#if s.fault}<StatusPill kind="critical" label={s.fault} />{/if}
							<Button size="sm" variant="danger" disabled={busy || !!deniedFor('substitute', s.name)} title={deniedFor('substitute', s.name) || undefined} onclick={() => stop(s)}>Stop</Button>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{:else if !readError}
		<p class="empty">Nothing is substituted.</p>
	{/if}

	{#if lapsed.length}
		<div class="lapsed" aria-label="Lapsed starts">
			{#each lapsed as l (l.id)}
				<div class="msg {l.ended ? 'error' : 'warn'}" role={l.ended ? 'alert' : 'status'}>
					<b>{l.name}</b> = {String(l.value)}{l.by ? ` (by ${l.by})` : ''}: {l.note}
				</div>
			{/each}
		</div>
	{/if}

	{#if waiting.length}
		<div class="ackbar">
			<span>{waiting.length} substitute{waiting.length === 1 ? '' : 's'} waiting for acknowledgement: {waiting.map((w) => w.name).join(', ')}</span>
			<Button size="sm" disabled={busy || waiting.some((w) => deniedFor('substitute', w.name))} title={waiting.map((w) => deniedFor('substitute', w.name)).find((r) => r) || undefined} onclick={() => ack(waiting.map((w) => w.id))}>Acknowledge {waiting.length === 1 ? '' : 'all'}</Button>
		</div>
	{/if}

	{#if canStart && rules.length}
		<form class="start" onsubmit={(e) => { e.preventDefault(); void start(); }}>
			<label>Tag
				<select bind:value={tag}>
					{#each rules as r (r.tag)}<option value={r.tag}>{r.tag}</option>{/each}
				</select>
			</label>
			{#if rule?.operatorValue}
				<label>Hold at <span class="q">(blank: {String(rule.value ?? '—')})</span>
					<input bind:value={valueText} inputmode="decimal" autocomplete="off" />
				</label>
			{:else if rule}
				<span class="fixed">Holds at <b>{String(rule.value ?? '—')}</b></span>
			{/if}
			<label>Seconds{#if maxSeconds(rule)} <span class="q">(max {maxSeconds(rule)})</span>{/if}
				<input type="number" min="1" max={maxSeconds(rule) || undefined} bind:value={seconds} />
			</label>
			<Button type="submit" variant="primary" disabled={busy || blocked || !!startError || !!startDenied} title={blocked ? 'An earlier start of this tag is not confirmed' : (startError ?? startDenied)}>Substitute…</Button>
			{#if rule?.ackOnStart}<span class="q">Needs acknowledgement after it starts.</span>{/if}
		</form>
	{/if}
</section>

<style>
	.subs { display: grid; gap: var(--space-3); font-size: var(--font-sm); }
	table { width: 100%; border-collapse: collapse; }
	th { text-align: left; font-size: var(--font-2xs); text-transform: uppercase; color: var(--muted); padding: var(--space-1) var(--space-2); }
	td { padding: var(--space-1) var(--space-2); border-top: 1px solid var(--border, color-mix(in srgb, currentColor 15%, transparent)); vertical-align: middle; }
	.num { font-family: var(--mono); font-variant-numeric: tabular-nums; }
	.name { font-weight: 600; }
	.q { color: var(--muted); font-size: var(--font-xs); }
	.act { display: flex; gap: var(--space-1); align-items: center; justify-content: flex-end; flex-wrap: wrap; }
	.empty { margin: 0; color: var(--muted); font-size: var(--font-xs); }
	.msg { margin: 0; padding: var(--space-2) var(--space-3); border-radius: var(--radius-sm); border: 1px solid currentColor; display: flex; gap: var(--space-3); justify-content: space-between; align-items: center; flex-wrap: wrap; }
	.msg.ok { color: var(--good); }
	.msg.warn { color: var(--warn); }
	.msg.error { color: var(--crit); }
	.actions { display: flex; gap: var(--space-1); }
	.ackbar { display: flex; gap: var(--space-3); align-items: center; justify-content: space-between; flex-wrap: wrap; padding: var(--space-2) var(--space-3); border-radius: var(--radius-sm); border: 1px solid var(--q-substituted); }
	.start { display: flex; gap: var(--space-3); align-items: end; flex-wrap: wrap; }
	.start label { display: grid; gap: 2px; font-size: var(--font-xs); color: var(--muted); }
	.start input, .start select { font: inherit; padding: var(--space-1) var(--space-2); border-radius: var(--radius-sm); min-width: 6rem; }
	.fixed { font-size: var(--font-xs); color: var(--muted); }
</style>
