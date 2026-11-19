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
		failureText,
		formatRemaining,
		keptText,
		unacked,
		validateStart,
		maxSeconds,
		type SubstituteEntry,
		type SubstituteResult,
		type SubstituteRule
	} from '../substitutes.js';
	import { confirm } from '../confirm.svelte.js';
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
	let unknownStart = $state<{ requestId: string; name: string; value: unknown; seconds: number } | null>(null);

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
		readAt = Date.now();
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
			notice = { kind: r.kind === 'unknown' || r.kind === 'unreachable' ? 'warn' : 'error', text: failureText(r) };
		}
		// Re-read after EVERY answer: for an unknown outcome this is the
		// operator's only way to see what actually happened.
		await refresh();
	}

	async function start(req?: { requestId: string; name: string; value: unknown; seconds: number }) {
		const name = req?.name ?? tag;
		const value = req ? req.value : rule?.operatorValue ? parseValue(valueText) : undefined;
		const secs = req?.seconds ?? seconds;
		if (!req && startError) {
			notice = { kind: 'error', text: startError };
			return;
		}
		const ok = await confirm({
			title: `Substitute ${name}?`,
			body: `The logic will run on ${value === undefined ? 'the rule\'s value' : String(value)} instead of the sensor's reading for up to ${formatRemaining(secs * 1000)}. It is not the plant's reading.`,
			confirmLabel: 'Substitute',
			danger: true
		});
		if (!ok) return;
		busy = true;
		const requestId = req?.requestId ?? client.newRequestId();
		const r = await client.start(name, value, secs, requestId);
		busy = false;
		unknownStart = !r.ok && r.retrySameId ? { requestId, name, value, seconds: secs } : null;
		await settle(r, `${name} substituted.`);
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

	async function ack(ids: string[]) {
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

	{#if unknownStart}
		<div class="msg warn" role="alert">
			<span>
				The start of <b>{unknownStart.name}</b> has an unknown outcome — it may still take effect. Check the list above, then
				retry (the same request, so it cannot start twice) or dismiss.
			</span>
			<span class="actions">
				<Button size="sm" disabled={busy} onclick={() => unknownStart && start(unknownStart)}>Retry same request</Button>
				<Button size="sm" variant="ghost" onclick={() => (unknownStart = null)}>Dismiss</Button>
			</span>
		</div>
	{/if}

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
							<Button size="sm" variant="danger" disabled={busy} onclick={() => stop(s)}>Stop</Button>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{:else if !readError}
		<p class="empty">Nothing is substituted.</p>
	{/if}

	{#if waiting.length}
		<div class="ackbar">
			<span>{waiting.length} substitute{waiting.length === 1 ? '' : 's'} waiting for acknowledgement: {waiting.map((w) => w.name).join(', ')}</span>
			<Button size="sm" disabled={busy} onclick={() => ack(waiting.map((w) => w.id))}>Acknowledge {waiting.length === 1 ? '' : 'all'}</Button>
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
				<label>Hold at
					<input bind:value={valueText} inputmode="decimal" autocomplete="off" />
				</label>
			{:else if rule}
				<span class="fixed">Holds at <b>{String(rule.value ?? '—')}</b></span>
			{/if}
			<label>Seconds{#if maxSeconds(rule)} <span class="q">(max {maxSeconds(rule)})</span>{/if}
				<input type="number" min="1" max={maxSeconds(rule) || undefined} bind:value={seconds} />
			</label>
			<Button type="submit" variant="primary" disabled={busy || !!startError} title={startError ?? ''}>Substitute…</Button>
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
