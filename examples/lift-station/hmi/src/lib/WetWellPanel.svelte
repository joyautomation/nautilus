<script lang="ts">
	// The wet well and the station's control: the level now and its recent
	// trend, the two float switches and the alarm, the duty/standby
	// sequence, then its setpoints: where the sequence (sequence.sfc)
	// starts and stops the lead and lag pumps, the level loop that sets
	// their speed in Auto (level.fbd, LIC-101), and the high level alarm.
	import { StatusPill, Trend } from '@joyautomation/nautilus-hmi';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import SetpointInput from '$lib/SetpointInput.svelte';
	import { num, bool, str, writeTag, levelBuf } from '$lib/client.svelte';

	type Setpoint = { tag: string; label: string; unit: string; max: number; step?: number };
	const LOOP: Setpoint[] = [
		{ tag: 'LevelSP', label: 'Level setpoint', unit: '%', max: 100, step: 0.5 },
		{ tag: 'MinSpeedHz', label: 'Min speed', unit: 'Hz', max: 60 },
		{ tag: 'MaxSpeedHz', label: 'Max speed', unit: 'Hz', max: 60 }
	];
	const ALARM: Setpoint[] = [{ tag: 'HighLevelAlmSP', label: 'High level', unit: '%', max: 100 }];
	const set = (tag: string) => (v: number) => writeTag(tag, v);
</script>

<div class="level">
	<span class="big">{num('LIT101_Level').toFixed(1)}<small>%</small></span>
	<span class="sub">LIT-101 level</span>
</div>

<Trend series={[{ name: 'Level', color: 'var(--s1)', points: levelBuf.points }]} unit="%" yMin={0} yMax={100} height={96} />

<div class="pills">
	<StatusPill kind={bool('LSHH101') ? 'critical' : 'off'} label="LSHH high-high" />
	<StatusPill kind={bool('LSLL101') ? 'warning' : 'off'} label="LSLL low-low" />
	<StatusPill kind={bool('HighLevelAlm') ? 'critical' : 'off'} label="high level alarm" />
	<StatusPill kind={bool('DryRunHold') ? 'warning' : 'off'} label="dry-run hold" />
</div>

<section>
	<h3>Sequence</h3>
	<p class="seq">
		<SeqStepTag text={str('SeqStep')} width={96} />
		<span>{bool('LeadIsP101') ? 'P-101' : 'P-102'} is lead</span>
	</p>
</section>

<section>
	<h3>Pump starts and stops</h3>
	<!-- On and off, for the lead pump and the lag pump: the four levels
	     sequence.sfc works from. -->
	<div class="grid" role="group" aria-label="Pump start and stop levels">
		<span></span><span class="col">Lead</span><span class="col">Lag</span>
		<span class="row">On at</span>
		<SetpointInput label="Lead on" unit="%" max={100} value={num('LeadOnLevel')} onsubmit={set('LeadOnLevel')} />
		<SetpointInput label="Lag on" unit="%" max={100} value={num('LagOnLevel')} onsubmit={set('LagOnLevel')} />
		<span class="row">Off at</span>
		<SetpointInput label="Lead off" unit="%" max={100} value={num('LeadOffLevel')} onsubmit={set('LeadOffLevel')} />
		<SetpointInput label="Lag off" unit="%" max={100} value={num('LagOffLevel')} onsubmit={set('LagOffLevel')} />
	</div>
</section>

<section>
	<h3>Level loop <span class="aside">speed in Auto</span></h3>
	{#each LOOP as s (s.tag)}
		<div class="line">
			<span>{s.label}</span>
			<SetpointInput label={s.label} unit={s.unit} min={0} max={s.max} step={s.step ?? 1} value={num(s.tag)} onsubmit={set(s.tag)} />
		</div>
	{/each}
</section>

<section>
	<h3>Alarm</h3>
	{#each ALARM as s (s.tag)}
		<div class="line">
			<span>{s.label}</span>
			<SetpointInput label={s.label} unit={s.unit} min={0} max={s.max} value={num(s.tag)} onsubmit={set(s.tag)} />
		</div>
	{/each}
</section>

<p class="hint">Enter writes a value; Escape puts the live one back.</p>

<style>
	.level {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin-bottom: 4px;
	}
	.big {
		font-family: var(--mono);
		font-size: 1.9rem;
		font-weight: 650;
		font-variant-numeric: tabular-nums;
		color: var(--ink);
	}
	.big small {
		font-size: 0.9rem;
		margin-left: 2px;
		color: var(--muted);
	}
	.sub {
		font-size: 0.78rem;
		color: var(--muted);
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		margin: 10px 0 4px;
	}
	section {
		padding: 12px 0;
		border-top: 1px solid var(--border);
		margin-top: 8px;
	}
	h3 {
		font-size: 0.72rem;
		font-weight: 700;
		letter-spacing: 0.07em;
		text-transform: uppercase;
		color: var(--muted);
		margin: 0 0 10px;
	}
	.aside {
		margin-left: 4px;
		font-weight: 500;
		letter-spacing: 0;
		text-transform: none;
	}
	.seq {
		display: flex;
		align-items: center;
		gap: 8px;
		margin: 0;
		font-size: 0.8rem;
		color: var(--ink-2);
	}
	.grid {
		display: grid;
		grid-template-columns: auto 1fr 1fr;
		align-items: center;
		gap: 8px 10px;
	}
	.col {
		font-size: 0.75rem;
		font-weight: 650;
		color: var(--ink-2);
	}
	.row,
	.line > span {
		font-size: 0.8rem;
		color: var(--ink-2);
	}
	.line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 3px 0;
	}
	.hint {
		margin: 4px 0 0;
		font-size: 0.72rem;
		color: var(--muted);
	}
</style>
