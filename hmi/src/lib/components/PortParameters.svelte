<script lang="ts">
	// The parameters of the device on an IO-Link port (#705): what its IODD
	// says they are (access, range, named values), what each holds now, and
	// the control to write one. All the contract handling (a refused write
	// changed nothing, an uncertain one is neither a failure nor a success)
	// lives in `PortParamClient` and its helpers; this component renders their
	// answers, and never says "failed" about a write whose outcome is unknown.
	//
	// A read says which device answered: a value from a device that is not the
	// one expected is shown as such. A write asks for confirmation first, so a
	// `<ConfirmDialog/>` must be mounted once in the app.
	import {
		PortParamClient,
		canRead,
		canWrite,
		failureText,
		identityText,
		identityTone,
		parseInput,
		valueLabel,
		type ParamAnswer,
		type ParamInfo,
		type ParamList
	} from '../iolink.js';
	import { confirm } from '../confirm.svelte.js';
	import { asksPassword, deniedFor } from '../auth.svelte.js';
	import Icon from './Icon.svelte';
	import StatusPill from './StatusPill.svelte';
	import Button from './Button.svelte';

	let {
		port,
		client = new PortParamClient(),
		canEdit = true
	}: {
		/** The port device's tag name: `LEVEL1`. */
		port: string;
		client?: PortParamClient;
		/** False hides the write controls (a read-only screen). */
		canEdit?: boolean;
	} = $props();

	let list = $state<ParamList | null>(null);
	let loadError = $state<string | null>(null);
	// The last answer for each parameter, by ref: its value and which device gave it.
	let answers = $state<Record<string, ParamAnswer>>({});
	let draft = $state<Record<string, string>>({});
	let busy = $state<string | null>(null);
	let notice = $state<{ kind: 'ok' | 'warn' | 'error'; text: string } | null>(null);

	// The identity of the port as the most recent answer (a read or a write) saw it.
	let latest = $state<ParamAnswer | undefined>(undefined);

	async function load() {
		const r = await client.list(port);
		if (!r.ok) {
			loadError = failureText(r.kind, r.message);
			return;
		}
		loadError = null;
		list = r.value;
	}

	$effect(() => {
		void port;
		void load();
	});

	async function read(p: ParamInfo) {
		busy = p.ref;
		const r = await client.read(port, p.ref);
		busy = null;
		if (!r.ok) {
			notice = { kind: r.kind === 'unreachable' ? 'warn' : 'error', text: `${p.id}: ${failureText(r.kind, r.message)}` };
			return;
		}
		answers[p.ref] = r.value;
		latest = r.value;
		notice = null;
	}

	// Writing a device parameter is the config permission, on the whole
	// controller, and an elevated one: a role without it sees Write disabled,
	// and a sign-in past its password window sees a lock (it will be asked
	// for the password) (#708). A convenience: the controller decides.
	const writeDenied = $derived(deniedFor('config'));
	const writeAsks = $derived(asksPassword('config'));

	async function write(p: ParamInfo) {
		const parsed = parseInput(p, draft[p.ref] ?? '');
		if (!parsed.ok) {
			notice = { kind: 'error', text: parsed.error };
			return;
		}
		const ok = await confirm({
			title: `Write ${p.id} on ${port}?`,
			body: `${p.id} = ${String(parsed.value)} is written to the device on ${list?.master ?? 'the master'}'s port. It is refused if that device is not the one expected, and it is recorded in the audit journal.`,
			confirmLabel: 'Write parameter',
			danger: true
		});
		if (!ok) return;
		busy = p.ref;
		const r = await client.write(port, p.ref, parsed.value);
		if (r.ok) {
			answers[p.ref] = r.value;
			latest = r.value;
			draft[p.ref] = '';
			notice = { kind: 'ok', text: `${p.id} now holds ${valueLabel(p, r.value.value)}.` };
		} else {
			notice = { kind: r.kind === 'uncertain' ? 'warn' : 'error', text: `${p.id}: ${failureText(r.kind, r.message)}` };
			// An uncertain write is settled by the operator's next look: read it back now.
			if (r.kind === 'uncertain') await read(p);
		}
		busy = null;
	}
</script>

<section class="params" aria-label={`Parameters of ${port}`}>
	{#if loadError}
		<p class="msg error" role="alert">{loadError}</p>
	{/if}

	{#if list}
		<header>
			<b>{list.port}</b> on <span class="master">{list.master}</span>
			{#if latest}
				<StatusPill
					kind={identityTone(latest.identity, latest.identityChecked) === 'ok' ? 'good' : identityTone(latest.identity, latest.identityChecked) === 'warn' ? 'serious' : 'critical'}
					label={latest.identity}
				/>
				<span class="q">{identityText(latest.identity, latest.identityChecked)}</span>
			{/if}
		</header>
	{/if}

	{#if notice}
		<p class="msg {notice.kind}" role={notice.kind === 'ok' ? 'status' : 'alert'}>{notice.text}</p>
	{/if}

	{#if list}
		<table>
			<thead>
				<tr><th>Parameter</th><th>Index</th><th>Access</th><th>Allowed</th><th>Value</th><th></th></tr>
			</thead>
			<tbody>
				{#each list.params as p (p.ref)}
					{@const a = answers[p.ref]}
					<tr>
						<td class="name">{p.name || p.id}<div class="q">{p.id}</div></td>
						<td class="num">{p.index}</td>
						<td>{p.access}</td>
						<td class="q">
							{#if p.range?.length}{p.range.join(', ')}{:else if p.values?.length}{p.values.map((v) => `${v.value}=${v.name}`).join(', ')}{:else}—{/if}
							{#if !p.carried}<div>Too long for this master.</div>{/if}
						</td>
						<td class="num">
							{#if a}
								{valueLabel(p, a.value)}
								{#if identityTone(a.identity, a.identityChecked) !== 'ok'}
									<StatusPill kind={identityTone(a.identity, a.identityChecked) === 'bad' ? 'critical' : 'serious'} label={a.identity} />
								{/if}
							{:else}—{/if}
						</td>
						<td class="act">
							{#if canRead(p)}
								<Button size="sm" variant="ghost" disabled={busy !== null} onclick={() => read(p)}>Read</Button>
							{/if}
							{#if canEdit && canWrite(p)}
								<input
									aria-label={`New value for ${p.id}`}
									bind:value={draft[p.ref]}
									autocomplete="off"
									inputmode="decimal"
									onkeydown={(e) => e.key === 'Enter' && write(p)}
								/>
								<Button
									size="sm"
									variant="danger"
									disabled={busy !== null || !(draft[p.ref] ?? '').trim() || !!writeDenied}
									title={writeDenied || (writeAsks ? 'asks for your password first' : undefined)}
									onclick={() => write(p)}
								>
									{#if writeAsks && !writeDenied}<Icon name="lock-closed" size={12} />{/if}Write…
								</Button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{:else if !loadError}
		<p class="empty">Reading the parameters of {port}…</p>
	{/if}
</section>

<style>
	.params { display: grid; gap: var(--space-3); font-size: var(--font-sm); }
	header { display: flex; gap: var(--space-2); align-items: center; flex-wrap: wrap; }
	.master { font-family: var(--mono); }
	table { width: 100%; border-collapse: collapse; }
	th { text-align: left; font-size: var(--font-2xs); text-transform: uppercase; color: var(--muted); padding: var(--space-1) var(--space-2); }
	td { padding: var(--space-1) var(--space-2); border-top: 1px solid var(--border, color-mix(in srgb, currentColor 15%, transparent)); vertical-align: middle; }
	.num { font-family: var(--mono); font-variant-numeric: tabular-nums; }
	.name { font-weight: 600; }
	.q { color: var(--muted); font-size: var(--font-xs); font-weight: 400; }
	.act { display: flex; gap: var(--space-1); align-items: center; justify-content: flex-end; flex-wrap: wrap; }
	.act input { font: inherit; padding: var(--space-1) var(--space-2); border-radius: var(--radius-sm); width: 7rem; }
	.empty { margin: 0; color: var(--muted); font-size: var(--font-xs); }
	.msg { margin: 0; padding: var(--space-2) var(--space-3); border-radius: var(--radius-sm); border: 1px solid currentColor; }
	.msg.ok { color: var(--good); }
	.msg.warn { color: var(--warn); }
	.msg.error { color: var(--crit); }
</style>
