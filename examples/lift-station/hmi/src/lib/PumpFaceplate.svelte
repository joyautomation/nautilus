<script lang="ts">
	// One pump's controls: running state, Hand/Off/Auto, speed, current,
	// starts and run hours, the fault states, and the shared fault reset.
	// `pump` is the tag prefix, P101 or P102 (motor.ld's instances).
	import { StatusPill, Button } from '@joyautomation/nautilus-hmi';
	import { numAt } from '@joyautomation/nautilus-hmi';
	import { rt, num, bool, writeTag, pulseReset } from '$lib/client.svelte';

	let { pump }: { pump: 'P101' | 'P102' } = $props();

	// HOA mode: 0 Off, 1 Hand, 2 Auto (permissives.ld / motor.ld).
	const MODES = [
		{ v: 0, label: 'Off' },
		{ v: 1, label: 'Hand' },
		{ v: 2, label: 'Auto' }
	];
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const running = $derived(bool(`${pump}_Running`));
</script>

<div class="state">
	<StatusPill kind={running ? 'good' : 'off'} label={running ? 'running' : 'stopped'} />
</div>

<div class="hoa" role="group" aria-label="Hand, off, auto">
	{#each MODES as m (m.v)}
		<button class:active={num(`${pump}_Mode`) === m.v} onclick={() => writeTag(`${pump}_Mode`, m.v)}>
			{m.label}
		</button>
	{/each}
</div>

<dl class="rows">
	<dt>Speed</dt><dd>{num(`${pump}_SpeedHz`).toFixed(0)} Hz</dd>
	<dt>Current</dt><dd>{num(`${pump}_Amps`).toFixed(1)} A</dd>
	<dt>Starts</dt><dd>{numAt(tags, `${pump}_Stats.Starts`, 0).toFixed(0)}</dd>
	<dt>Run hours</dt><dd>{numAt(tags, `${pump}_Stats.Hours`, 0).toFixed(1)}</dd>
</dl>

<div class="pills">
	<StatusPill kind={bool(`${pump}_FailToRun`) ? 'critical' : 'off'} label="fail to run" />
	<StatusPill kind={bool(`${pump}_LockedOut`) ? 'critical' : 'off'} label="locked out" />
	<StatusPill kind={bool(`${pump}_SealFail`) ? 'warning' : 'off'} label="seal fail" />
	<StatusPill kind={bool(`${pump}_OverTemp`) ? 'warning' : 'off'} label="over temp" />
	<StatusPill kind={bool(`${pump}_VfdFault`) ? 'critical' : 'off'} label="VFD fault" />
</div>

<Button size="sm" variant="secondary" onclick={pulseReset}>Reset faults</Button>
<p class="note">Reset clears both pumps' latched faults.</p>

<style>
	.state {
		margin-bottom: 12px;
	}
	.hoa {
		display: flex;
		gap: 4px;
		margin-bottom: 14px;
	}
	.hoa button {
		flex: 1;
		border: 1px solid var(--border);
		background: var(--surface-2);
		color: var(--ink-2);
		border-radius: 6px;
		padding: 7px 8px;
		font: inherit;
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
	}
	.hoa button.active {
		background: var(--s1);
		color: var(--bg);
		border-color: var(--s1);
	}
	.rows {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 6px 10px;
		margin: 0 0 12px;
		font-size: 12.5px;
	}
	dt {
		color: var(--ink-2);
	}
	dd {
		margin: 0;
		font-family: var(--mono);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
		color: var(--ink);
		text-align: right;
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		margin-bottom: 14px;
	}
	.note {
		margin: 8px 0 0;
		font-size: 0.75rem;
		color: var(--muted);
	}
</style>
