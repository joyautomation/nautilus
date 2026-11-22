<script lang="ts">
	// Equipment out of service (#715 PR 5, #616): the windows standing, the
	// soonest to end first, each with why, who and until and a Return to
	// service; and "Take out of service" for a piece of equipment: a reason
	// (required), a duration of a week at most. Taking out needs a fresh
	// password; the kit's fetch asks for it. Props in, callbacks out.
	import type { AlarmInstance } from '../alarms.svelte.js';
	import {
		OOS_DURATIONS_S,
		OOS_REASON_MAX,
		checkOutOfService,
		equipmentChoices,
		soonestFirst,
		timeLeft,
		type OutOfService
	} from '../outofservice.js';
	import { currentAuth, deniedFor, asksPassword } from '../auth.svelte.js';
	import { recordedAs, getOperator } from '../confirm.js';
	import Button from './Button.svelte';
	import Modal from './Modal.svelte';

	let {
		alarms,
		equipmentList = undefined,
		windows,
		now = Date.now(),
		ontake,
		onreturn,
		operator = ''
	}: {
		/** The instance list: the equipment to choose from comes from it. */
		alarms: AlarmInstance[];
		/** `alarmClient.equipment`: every piece of equipment, quiet ones too. Defaults to the alarms' own. */
		equipmentList?: string[];
		/** `alarmClient.outOfService`. */
		windows: OutOfService[];
		now?: number;
		/** Resolves true when the controller took it (the form closes). */
		ontake: (equipment: string, seconds: number, reason: string, by: string) => Promise<boolean>;
		onreturn: (equipment: string, by: string) => Promise<boolean> | void;
		operator?: string;
	} = $props();

	const choices = $derived(equipmentList?.length ? equipmentList : equipmentChoices(alarms));
	const rows = $derived(soonestFirst(windows));
	const free = $derived(choices.filter((e) => !rows.some((w) => w.equipment.toLowerCase() === e.toLowerCase())));
	const by = (typed: string) => recordedAs(currentAuth()?.user).by(typed || getOperator() || operator);

	let open = $state(false);
	let equipment = $state('');
	let seconds = $state(OOS_DURATIONS_S[0].seconds);
	let reason = $state('');
	let problem = $state('');
	let busy = $state(false);

	function begin(eq = '') {
		equipment = eq || free[0] || choices[0] || '';
		seconds = OOS_DURATIONS_S[0].seconds;
		reason = '';
		problem = '';
		open = true;
	}

	async function submit() {
		problem = checkOutOfService(equipment, seconds, reason);
		if (problem) return;
		busy = true;
		try {
			if (await ontake(equipment, seconds, reason.trim(), by(''))) open = false;
			else problem = 'the controller did not take it: see the message above the table';
		} finally {
			busy = false;
		}
	}

	// A convenience: the controller decides.
	const denied = $derived(deniedFor('out-of-service'));
	const needsPassword = $derived(asksPassword('out-of-service'));
	const fmt = (ms: number) =>
		new Date(ms).toLocaleString(undefined, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
</script>

<section class="oos" aria-label="Equipment out of service">
	<header>
		<h2>Out of service</h2>
		<Button size="sm" icon="clock" disabled={!choices.length || !!denied} title={denied || undefined} onclick={() => begin()}>
			Take out of service
		</Button>
	</header>

	{#if rows.length}
		<table>
			<thead>
				<tr><th>Equipment</th><th>Why</th><th>By</th><th>Until</th><th>Left</th><th></th></tr>
			</thead>
			<tbody>
				{#each rows as w (w.equipment)}
					<tr>
						<td>{w.equipment}<span class="n"> · {w.alarms.length} alarm{w.alarms.length === 1 ? '' : 's'}</span></td>
						<td>{w.reason}</td>
						<td>{w.by}</td>
						<td>{fmt(w.untilMs)}</td>
						<td class="num">{timeLeft(w.untilMs, now)}</td>
						<td>
							<Button size="sm" variant="secondary" icon="arrow-path" onclick={() => onreturn(w.equipment, by(''))}>
								Return to service
							</Button>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{:else}
		<p class="none">Nothing is out of service.</p>
	{/if}
</section>

<Modal bind:open title="Take equipment out of service" size="sm">
	{#snippet children()}
		<p class="warn">Every alarm of this equipment is hidden until the time is up or it is returned to service.</p>
		<label class="field">
			<span>Equipment</span>
			<select bind:value={equipment}>
				{#each choices as e (e)}
					<option value={e}>{e}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			<span>For</span>
			<select bind:value={seconds}>
				{#each OOS_DURATIONS_S as d (d.seconds)}
					<option value={d.seconds}>{d.label}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			<span>Reason (required)</span>
			<input type="text" bind:value={reason} maxlength={OOS_REASON_MAX} placeholder="e.g. seal replacement, WO 1234" />
		</label>
		{#if currentAuth()?.user}
			<p class="field">Recorded as {currentAuth()?.user}, the signed-in user.{needsPassword ? ' Your password is asked next.' : ''}</p>
		{/if}
		{#if problem}<p class="problem" role="alert">{problem}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
		<Button variant="primary" disabled={busy} onclick={submit}>Take out of service</Button>
	{/snippet}
</Modal>

<style>
	.oos {
		display: grid;
		gap: var(--space-2);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	h2 {
		margin: 0;
		font-size: var(--font-md, 1rem);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--font-xs);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-1) var(--space-2);
		border-bottom: 1px solid var(--border);
	}
	.num {
		font-variant-numeric: tabular-nums;
	}
	.n,
	.none {
		color: var(--muted);
	}
	.none {
		margin: 0;
		font-size: var(--font-xs);
	}
	.field {
		display: grid;
		gap: var(--space-1);
		margin-bottom: var(--space-2);
		font-size: var(--font-xs);
	}
	.field select,
	.field input {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--ink);
		font: inherit;
		padding: var(--space-1) var(--space-2);
	}
	.warn {
		color: var(--muted);
		font-size: var(--font-xs);
		margin: 0 0 var(--space-2);
	}
	.problem {
		color: var(--danger, #c33);
		font-size: var(--font-xs);
		margin: 0;
	}
</style>
