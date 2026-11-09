<script lang="ts">
	// The wet well and the station's control: the level now and its recent
	// trend, the two float switches, the duty/standby sequence, and the level
	// setpoints the sequence (sequence.sfc) and the speed loop (level.fbd)
	// work from.
	import { StatusPill, NumberField, Trend } from '@joyautomation/nautilus-hmi';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import { num, bool, str, writeTag, levelBuf } from '$lib/client.svelte';

	type Setpoint = { tag: string; label: string; unit: string; max: number; step?: number };
	// Where the duty/standby sequence starts and stops the pumps
	// (sequence.sfc), the level loop that sets their speed in Auto
	// (level.fbd, LIC-101), and the alarm.
	const GROUPS: { title: string; items: Setpoint[] }[] = [
		{
			title: 'Pump starts and stops',
			items: [
				{ tag: 'LagOnLevel', label: 'Lag on', unit: '%', max: 100 },
				{ tag: 'LeadOnLevel', label: 'Lead on', unit: '%', max: 100 },
				{ tag: 'LagOffLevel', label: 'Lag off', unit: '%', max: 100 },
				{ tag: 'LeadOffLevel', label: 'Lead off', unit: '%', max: 100 }
			]
		},
		{
			title: 'Level loop (speed in Auto)',
			items: [
				{ tag: 'LevelSP', label: 'Level SP', unit: '%', max: 100, step: 0.5 },
				{ tag: 'MinSpeedHz', label: 'Min speed', unit: 'Hz', max: 60 },
				{ tag: 'MaxSpeedHz', label: 'Max speed', unit: 'Hz', max: 60 }
			]
		},
		{ title: 'Alarm', items: [{ tag: 'HighLevelAlmSP', label: 'High level', unit: '%', max: 100 }] }
	];
</script>

<div class="level">
	<span class="big">{num('LIT101_Level').toFixed(1)}<small>%</small></span>
	<span class="sub">LIT-101 level</span>
</div>

<Trend series={[{ name: 'Level', color: 'var(--s1)', points: levelBuf.points }]} unit="%" yMin={0} yMax={100} height={110} />

<div class="pills">
	<StatusPill kind={bool('LSHH101') ? 'critical' : 'off'} label="LSHH high-high float" />
	<StatusPill kind={bool('LSLL101') ? 'warning' : 'off'} label="LSLL low-low float" />
	<StatusPill kind={bool('HighLevelAlm') ? 'critical' : 'off'} label="high level alarm" />
</div>

<h3>Sequence</h3>
<p class="seq">
	<SeqStepTag text={str('SeqStep')} width={110} />
	<span>{bool('LeadIsP101') ? 'P-101' : 'P-102'} is lead</span>
</p>

{#each GROUPS as g (g.title)}
	<h3>{g.title}</h3>
	<div class="sp">
		{#each g.items as s (s.tag)}
			<NumberField
				label={s.label}
				unit={s.unit}
				value={num(s.tag)}
				min={0}
				max={s.max}
				step={s.step ?? 1}
				onsubmit={(v) => writeTag(s.tag, v)}
			/>
		{/each}
	</div>
{/each}

<style>
	.level {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin-bottom: 6px;
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
		margin: 10px 0 14px;
	}
	h3 {
		font-size: 0.72rem;
		font-weight: 700;
		letter-spacing: 0.07em;
		text-transform: uppercase;
		color: var(--muted);
		margin: 0 0 8px;
	}
	.seq {
		display: flex;
		align-items: center;
		gap: 8px;
		margin: 0 0 14px;
		font-size: 0.8rem;
		color: var(--ink-2);
	}
	.sp {
		display: grid;
		gap: 10px;
		margin-bottom: 16px;
	}
</style>
