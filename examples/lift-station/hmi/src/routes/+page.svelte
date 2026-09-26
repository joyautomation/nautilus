<script lang="ts">
	// Lift station operator screen — one page: the wet-well mimic, an alarm
	// banner + ack, two pump faceplates (HOA, running, speed, amps,
	// hours/starts, fail-to-run/locked-out, reset faults), the level
	// setpoints, a level trend, the field-driver status panel and scan
	// diagnostics. Every value on screen comes from a tag in
	// `/api/stream` (nautilus.yaml / field.yaml's tag model); nothing here
	// invents a value the controller doesn't publish.
	import {
		Mimic,
		AlarmBanner,
		AckButton,
		StatusPill,
		NumberField,
		Button,
		Trend,
		DriverStatusPanel,
		ScanDiagnostics,
		numAt,
		type MimicDoc
	} from '@joyautomation/nautilus-hmi';
	// lift-station.mimic.json lives at the nautilus PROJECT root — see
	// hmi/vite.config.ts's server.fs.allow and the README for why.
	import mimicDoc from '../../../lift-station.mimic.json';
	import SubmersiblePump from '$lib/SubmersiblePump.svelte';
	import FloatSwitch from '$lib/FloatSwitch.svelte';
	import ToProcess from '$lib/ToProcess.svelte';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import { rt, alarms, levelBuf, writeTag } from '$lib/client.svelte';

	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const num = (k: string, d = 0) => (typeof tags[k] === 'number' ? (tags[k] as number) : d);
	const bool = (k: string) => tags[k] === true;
	const str = (k: string) => (typeof tags[k] === 'string' ? (tags[k] as string) : '');

	// HOA mode: 0 Off, 1 Hand, 2 Auto (permissives.ld / motor.ld).
	const MODES: { v: number; label: string }[] = [
		{ v: 0, label: 'Off' },
		{ v: 1, label: 'Hand' },
		{ v: 2, label: 'Auto' }
	];

	// ResetFaults is a PULSE input (motor.ld's `reset` rung, and the
	// `lockout` rung's own gated reset) — hold it true just long enough for
	// one scan to see it, then drop it, so the HMI button behaves like a
	// momentary pushbutton rather than a maintained switch left ON.
	async function pulseReset() {
		await writeTag('ResetFaults', true);
		setTimeout(() => writeTag('ResetFaults', false), 300);
	}
</script>

<div class="page">
	<header>
		<div class="title">
			<h1>Lift Station</h1>
			<p class="subtle">WW-101 duplex sewage lift station — P-101 / P-102 duty/standby</p>
		</div>
		<div class="head-right">
			<AlarmBanner summary={alarms.summary} />
			<StatusPill kind={rt.connected ? 'good' : 'critical'} label={rt.connected ? 'Connected' : 'Offline'} />
		</div>
	</header>

	{#if !rt.frame}
		<div class="waiting">
			<p>Connecting to the controller…</p>
			<p class="hint">
				Run <code>naut run .</code> in <code>examples/lift-station</code>, then this page
				follows its <code>/api/stream</code>.
			</p>
		</div>
	{:else}
		<section class="card mimic-card">
			<div class="mimic-head">
				<h2>Process mimic</h2>
				<AckButton alarms={alarms.active} ids={['*']} onack={(ids, by) => alarms.ack(ids, by)} />
			</div>
			<Mimic
				doc={mimicDoc as unknown as MimicDoc}
				{tags}
				registry={{ SubmersiblePump, FloatSwitch, ToProcess, SeqStepTag }}
			/>
		</section>

		<div class="grid">
			<!-- P-101 faceplate -->
			<section class="card faceplate">
				<div class="fp-head">
					<h2>P-101</h2>
					<StatusPill kind={bool('P101_Running') ? 'good' : 'off'} label={bool('P101_Running') ? 'running' : 'stopped'} />
				</div>
				<div class="hoa">
					{#each MODES as m (m.v)}
						<button class:active={num('P101_Mode') === m.v} onclick={() => writeTag('P101_Mode', m.v)}>
							{m.label}
						</button>
					{/each}
				</div>
				<div class="rows">
					<div class="row"><span>Speed</span><b>{num('P101_SpeedHz').toFixed(0)} Hz</b></div>
					<div class="row"><span>Current</span><b>{num('P101_Amps').toFixed(1)} A</b></div>
					<div class="row"><span>Starts</span><b>{numAt(tags, 'P101_Stats.Starts', 0).toFixed(0)}</b></div>
					<div class="row"><span>Run hours</span><b>{numAt(tags, 'P101_Stats.Hours', 0).toFixed(1)}</b></div>
				</div>
				<div class="pills">
					<StatusPill kind={bool('P101_FailToRun') ? 'critical' : 'off'} label="fail to run" />
					<StatusPill kind={bool('P101_LockedOut') ? 'critical' : 'off'} label="locked out" />
					<StatusPill kind={bool('P101_SealFail') ? 'warning' : 'off'} label="seal fail" />
					<StatusPill kind={bool('P101_OverTemp') ? 'warning' : 'off'} label="over temp" />
					<StatusPill kind={bool('P101_VfdFault') ? 'critical' : 'off'} label="VFD fault" />
				</div>
				<Button size="sm" variant="secondary" onclick={pulseReset}>Reset faults</Button>
			</section>

			<!-- P-102 faceplate -->
			<section class="card faceplate">
				<div class="fp-head">
					<h2>P-102</h2>
					<StatusPill kind={bool('P102_Running') ? 'good' : 'off'} label={bool('P102_Running') ? 'running' : 'stopped'} />
				</div>
				<div class="hoa">
					{#each MODES as m (m.v)}
						<button class:active={num('P102_Mode') === m.v} onclick={() => writeTag('P102_Mode', m.v)}>
							{m.label}
						</button>
					{/each}
				</div>
				<div class="rows">
					<div class="row"><span>Speed</span><b>{num('P102_SpeedHz').toFixed(0)} Hz</b></div>
					<div class="row"><span>Current</span><b>{num('P102_Amps').toFixed(1)} A</b></div>
					<div class="row"><span>Starts</span><b>{numAt(tags, 'P102_Stats.Starts', 0).toFixed(0)}</b></div>
					<div class="row"><span>Run hours</span><b>{numAt(tags, 'P102_Stats.Hours', 0).toFixed(1)}</b></div>
				</div>
				<div class="pills">
					<StatusPill kind={bool('P102_FailToRun') ? 'critical' : 'off'} label="fail to run" />
					<StatusPill kind={bool('P102_LockedOut') ? 'critical' : 'off'} label="locked out" />
					<StatusPill kind={bool('P102_SealFail') ? 'warning' : 'off'} label="seal fail" />
					<StatusPill kind={bool('P102_OverTemp') ? 'warning' : 'off'} label="over temp" />
					<StatusPill kind={bool('P102_VfdFault') ? 'critical' : 'off'} label="VFD fault" />
				</div>
				<Button size="sm" variant="secondary" onclick={pulseReset}>Reset faults</Button>
			</section>

			<!-- Level setpoints -->
			<section class="card">
				<h2>Level setpoints</h2>
				<div class="sp-grid">
					<NumberField label="Lead on" unit="%" value={num('LeadOnLevel')} min={0} max={100} step={1} onsubmit={(v) => writeTag('LeadOnLevel', v)} />
					<NumberField label="Lag on" unit="%" value={num('LagOnLevel')} min={0} max={100} step={1} onsubmit={(v) => writeTag('LagOnLevel', v)} />
					<NumberField label="Lag off" unit="%" value={num('LagOffLevel')} min={0} max={100} step={1} onsubmit={(v) => writeTag('LagOffLevel', v)} />
					<NumberField label="Lead off" unit="%" value={num('LeadOffLevel')} min={0} max={100} step={1} onsubmit={(v) => writeTag('LeadOffLevel', v)} />
					<NumberField label="Level SP (speed loop)" unit="%" value={num('LevelSP')} min={0} max={100} step={0.5} onsubmit={(v) => writeTag('LevelSP', v)} />
				</div>
				<p class="subtle small">
					Sequence: <SeqStepTag text={str('SeqStep')} width={110} /> · duty pointer
					{bool('LeadIsP101') ? 'P-101' : 'P-102'} is lead
				</p>
			</section>

			<!-- Level trend -->
			<section class="card trendcard">
				<h2>Wet-well level · LIT-101</h2>
				<Trend
					series={[{ name: 'Level', color: 'var(--s1)', points: levelBuf.points }]}
					unit="%"
					yMin={0}
					yMax={100}
					height={190}
				/>
			</section>

			<!-- Driver status -->
			<section class="card">
				<h2>Driver status</h2>
				<DriverStatusPanel drivers={rt.frame?.drivers ?? []} />
			</section>

			<!-- Scan diagnostics -->
			<section class="card">
				<h2>Scan diagnostics</h2>
				<ScanDiagnostics scan={rt.frame.scan} />
			</section>
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 1280px;
		margin: 0 auto;
		padding: 20px 24px 48px;
	}
	header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 16px;
		flex-wrap: wrap;
		margin-bottom: 16px;
	}
	h1 {
		margin: 0 0 0.2rem;
		font-size: 1.5rem;
		font-weight: 680;
	}
	h2 {
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0.07em;
		text-transform: uppercase;
		color: var(--muted);
		margin: 0 0 0.7rem;
	}
	.subtle {
		color: var(--muted);
		font-size: 0.85rem;
	}
	.subtle.small {
		font-size: 0.78rem;
		margin: 10px 0 0;
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}
	.head-right {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		padding: 16px;
	}
	.mimic-card {
		margin-bottom: 16px;
	}
	.mimic-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 4px;
	}
	.mimic-head h2 {
		margin: 0;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: 16px;
	}
	.trendcard {
		grid-column: span 2;
		min-width: 320px;
	}
	.fp-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 10px;
	}
	.fp-head h2 {
		margin: 0;
	}
	.hoa {
		display: flex;
		gap: 4px;
		margin-bottom: 12px;
	}
	.hoa button {
		flex: 1;
		border: 1px solid var(--border);
		background: var(--surface-2);
		color: var(--ink-2);
		border-radius: 6px;
		padding: 6px 8px;
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
		gap: 6px;
		margin-bottom: 10px;
	}
	.row {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		font-size: 12.5px;
		color: var(--ink-2);
	}
	.row b {
		font-family: var(--mono);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
		color: var(--ink);
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		margin-bottom: 12px;
	}
	.sp-grid {
		display: grid;
		gap: 10px;
	}
	.waiting {
		padding: 4rem 1rem;
		text-align: center;
		color: var(--muted);
	}
	.waiting .hint {
		font-size: 0.85rem;
	}
	code {
		font-family: var(--mono);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 4px;
		padding: 0.05rem 0.3rem;
		color: var(--accent, var(--s1));
	}
	@media (max-width: 720px) {
		.trendcard {
			grid-column: span 1;
		}
	}
</style>
