<script lang="ts">
	// Process Overview: the mimic and gauges/trend in a main column beside a
	// right rail of operator panels — every value on screen is fed by a
	// controller tag from `/api/stream` (proxied to the controller; the
	// header pills and connection plumbing live in +layout.svelte). The
	// P&ID mimic stays pure data (heated-tank.mimic.json) rendered live by
	// <Mimic>; clicking equipment opens its device faceplate.
	import {
		Trend,
		StatusPill,
		NumberField,
		Mimic,
		Faceplate,
		Tabs,
		Gauge,
		type MimicDoc,
		type MimicEquipment
	} from '@joyautomation/nautilus-hmi';
	import mimicDoc from './heated-tank.mimic.json';
	// App-supplied scene dressing (not kit equipment) — passed to <Mimic>
	// via `registry` and placed in the doc as components "Supply"/"ToProcess".
	import Supply from '$lib/Supply.svelte';
	import ToProcess from '$lib/ToProcess.svelte';
	import { rt, tempBuf, levelBuf, writeTag } from '$lib/client.svelte';

	// Tag accessors over the latest frame, with sane fallbacks.
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const num = (k: string, d = 0) => (typeof tags[k] === 'number' ? (tags[k] as number) : d);
	const bool = (k: string) => tags[k] === true;
	const str = (k: string) => (typeof tags[k] === 'string' ? (tags[k] as string) : '');

	// Alarm state.
	const hiAlm = $derived(bool('HiTempAlm'));
	const lowAlm = $derived(bool('TempLowAlm'));
	const horn = $derived(bool('Horn'));

	// This screen is drawn for the heated-tank program. When the connected
	// controller doesn't publish those tags, say so and dim the panels —
	// an absent tag must never masquerade as a confident 0.0.
	const hasTankTags = $derived('TempC' in tags && 'LevelPct' in tags);

	// Faceplates: clicking equipment on the mimic opens its device popup —
	// the PLC-HMI pattern (monitoring | control, tabbed subviews).
	let faceplate = $state<string | null>(null);
	let faceplateTab = $state(0);
	function openFaceplate(eq: MimicEquipment) {
		faceplate = eq.id;
		faceplateTab = 0;
	}
</script>

{#if rt.frame}
	<h1>Process Overview</h1>
	<p class="subtle" style="margin-top: 0">
		Heated surge tank T-101 — pump P-101 maintains level, PI loop TIC-101 holds temperature
		against the process demand drawn through DV-101.
	</p>

	{#if !hasTankTags}
		<div class="notank" role="status">
			This controller doesn't publish the heated-tank tags (<code>TempC</code>,
			<code>LevelPct</code>, …) — the panels below are idle, not live zeros.
			Run <code>examples/heated-tank-nogo</code> and point <code>CONTROLLER_URL</code>
			at it to see them move.
		</div>
	{/if}

	<div class="top" class:idle={!hasTankTags}>
		<div class="main-col">
			<section class="card scene-card">
				<!-- The P&ID mimic: pure data (heated-tank.mimic.json) rendered
				     live by <Mimic> — equipment placement, pipe routing, tag
				     bindings, and the readout chips all live in the document,
				     not this page. -->
				<Mimic
					doc={mimicDoc as unknown as MimicDoc}
					{tags}
					registry={{ Supply, ToProcess }}
					onequipmentclick={openFaceplate}
				/>
			</section>

			<div class="bottom">
				<div class="card gauges">
					<Gauge
						value={num('TempC')}
						min={30}
						max={90}
						unit="°C"
						label="Temperature"
						color="var(--s1)"
						setpoint={typeof tags.TempSP === 'number' ? (tags.TempSP as number) : undefined}
					/>
					<Gauge value={num('LevelPct')} min={0} max={100} unit="%" label="Level" color="var(--s3)" decimals={0} />
					<Gauge value={num('HeaterKw')} min={0} max={240} unit="kW" label="Heater" color="var(--s5)" decimals={0} />
				</div>
				<div class="card trendcard">
					<h2>Tank level · pump cycling</h2>
					<Trend
						series={[{ name: 'Level', color: 'var(--s3)', points: levelBuf.points }]}
						unit="%"
						yMin={0}
						yMax={100}
						height={190}
					/>
				</div>
			</div>
		</div>

		<aside class="side">
			<!-- TIC-101 panel, composed from kit pieces: the controller exposes
			     no auto/man mode or manual-CV tags, so there are deliberately no
			     mode buttons here — PV/SP/CV plus setpoint entry is the whole
			     surface the tags support. -->
			<div class="card">
				<h2>TIC-101 · temperature</h2>
				<div class="rows">
					<div class="row"><span>PV</span><b>{num('TempC').toFixed(1)} °C</b></div>
					<div class="row"><span>SP</span><b>{num('TempSP', 65).toFixed(1)} °C</b></div>
					<div class="row"><span>CV</span><b>{num('Heater').toFixed(0)} %</b></div>
					<div class="row"><span>ERR</span><b>{(num('TempSP', 65) - num('TempC')).toFixed(1)} °C</b></div>
				</div>
				<NumberField
					label="Setpoint"
					unit="°C"
					value={num('TempSP', 65)}
					min={0}
					max={100}
					step={0.5}
					onsubmit={(v) => writeTag('TempSP', v)}
				/>
				<button class="btn" class:on={bool('EcoMode')} onclick={() => writeTag('EcoMode', !bool('EcoMode'))}>
					Eco mode {bool('EcoMode') ? 'on' : 'off'}
				</button>
			</div>

			<!-- No manual pump command or speed tag exists: the panel shows the
			     seal-in state and the (writable) auto band, nothing more. -->
			<div class="card">
				<h2>Pump P-101</h2>
				<StatusPill kind={bool('PumpRun') ? 'good' : 'off'} label={bool('PumpRun') ? 'running' : 'stopped'} />
				<p class="subtle" style="margin: 8px 0 0">
					Starts &lt; {num('PumpStartLevel', 40).toFixed(0)}% · stops &gt; {num('PumpStopLevel', 75).toFixed(0)}% level
				</p>
				<NumberField
					label="Start level"
					unit="%"
					value={num('PumpStartLevel', 40)}
					min={0}
					max={100}
					onsubmit={(v) => writeTag('PumpStartLevel', v)}
				/>
				<NumberField
					label="Stop level"
					unit="%"
					value={num('PumpStopLevel', 75)}
					min={0}
					max={100}
					onsubmit={(v) => writeTag('PumpStopLevel', v)}
				/>
			</div>

			<div class="card">
				<h2>Process demand</h2>
				<input
					type="range"
					min="0"
					max="100"
					value={num('Demand', 60)}
					onchange={(e) => writeTag('Demand', Number((e.currentTarget as HTMLInputElement).value))}
				/>
				<div class="subtle">
					demand {num('Demand', 60).toFixed(0)}% · DV-101 at {num('DV101').toFixed(0)}%
				</div>
			</div>

			<div class="card">
				<h2>Alarms</h2>
				<div class="pills">
					<StatusPill kind={hiAlm ? 'critical' : 'off'} label="hi temp" />
					<StatusPill kind={lowAlm ? 'warning' : 'off'} label="lo temp" />
					<StatusPill kind={horn ? 'critical' : 'off'} label={horn ? 'HORN' : 'horn clear'} />
				</div>
				<button class="btn ack" disabled={!horn} onclick={() => writeTag('HornAck', true)}>
					Acknowledge horn
				</button>
				{#if str('Status')}<p class="statusline">{str('Status')}</p>{/if}
			</div>
		</aside>
	</div>
{:else}
	<div class="waiting">
		<p>Connecting to the controller…</p>
		<p class="hint">
			Run one with <code>nautilus run</code> (e.g. in <code>examples/heated-tank-nogo</code>),
			then this page follows its <code>/api/stream</code>.
		</p>
	</div>
{/if}

{#if faceplate === 'T101'}
	<Faceplate title="T-101 · Heated surge tank" subtitle="Main/T101" onclose={() => (faceplate = null)}>
		<div class="fp-cols">
			<div class="fp-panel">
				<span class="fp-head">Monitoring</span>
				<div class="fp-rows">
					<div class="fp-row"><span>Level</span><b>{num('LevelPct').toFixed(1)} %</b></div>
					<div class="fp-row"><span>Temperature</span><b>{num('TempC').toFixed(1)} °C</b></div>
					<div class="fp-row"><span>Heater output</span><b>{num('Heater').toFixed(0)} %</b></div>
					<div class="fp-row"><span>Rate of change</span><b>{num('TempRate').toFixed(2)} °C/min</b></div>
				</div>
				<div class="fp-pills">
					<StatusPill kind={hiAlm ? 'critical' : 'off'} label="hi temp" />
					<StatusPill kind={lowAlm ? 'warning' : 'off'} label="lo temp" />
					<StatusPill kind={horn ? 'critical' : 'off'} label="horn" />
				</div>
			</div>
			<div class="fp-panel">
				<span class="fp-head">Control</span>
				<NumberField
					label="Temp setpoint"
					unit="°C"
					value={num('TempSP', 65)}
					min={0}
					max={100}
					step={0.5}
					onsubmit={(v) => writeTag('TempSP', v)}
				/>
				<button class="btn" class:on={bool('EcoMode')} onclick={() => writeTag('EcoMode', !bool('EcoMode'))}>
					Eco mode {bool('EcoMode') ? 'on' : 'off'}
				</button>
				<button class="btn ack" disabled={!horn} onclick={() => writeTag('HornAck', true)}>
					Acknowledge horn
				</button>
			</div>
		</div>
		<Tabs tabs={['Trend', 'Tuning']} bind:active={faceplateTab} />
		{#if faceplateTab === 0}
			<Trend
				series={[{ name: 'Temp', color: 'var(--s1)', points: tempBuf.points }]}
				unit="°C"
				height={150}
			/>
		{:else}
			<div class="fp-tuning">
				<NumberField label="Kp" value={num('Kp', 12)} min={0} max={100} step={0.5} onsubmit={(v) => writeTag('Kp', v)} />
				<NumberField label="Ki" unit="1/s" value={num('Ki', 0.15)} min={0} max={10} step={0.01} onsubmit={(v) => writeTag('Ki', v)} />
				<NumberField label="Eco setpoint" unit="°C" value={num('TempSPEco', 55)} min={0} max={100} step={0.5} onsubmit={(v) => writeTag('TempSPEco', v)} />
			</div>
		{/if}
	</Faceplate>
{:else if faceplate === 'P101'}
	<Faceplate title="P-101 · Feed pump" subtitle="Main/P101" onclose={() => (faceplate = null)}>
		<div class="fp-cols">
			<div class="fp-panel">
				<span class="fp-head">Monitoring</span>
				<div class="fp-center">
					<Gauge value={num('LevelPct')} min={0} max={100} unit="%" label="Tank level" width={150} />
				</div>
				<div class="fp-pills">
					<StatusPill kind={bool('PumpRun') ? 'good' : 'off'} label={bool('PumpRun') ? 'running' : 'stopped'} />
				</div>
			</div>
			<div class="fp-panel">
				<span class="fp-head">Control</span>
				<p class="fp-note">Seal-in latch: starts at the low mark, drops out at the high mark.</p>
				<NumberField
					label="Start level"
					unit="%"
					value={num('PumpStartLevel', 40)}
					min={0}
					max={100}
					onsubmit={(v) => writeTag('PumpStartLevel', v)}
				/>
				<NumberField
					label="Stop level"
					unit="%"
					value={num('PumpStopLevel', 75)}
					min={0}
					max={100}
					onsubmit={(v) => writeTag('PumpStopLevel', v)}
				/>
			</div>
		</div>
	</Faceplate>
{/if}

<style>
	h1 {
		margin: 0 0 0.35rem;
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
	/* overview grid — mirrors mini-scada's Process Overview */
	.top {
		display: flex;
		gap: 16px;
		align-items: flex-start;
		flex-wrap: wrap;
		margin-top: 16px;
	}
	.main-col {
		flex: 1;
		min-width: 540px;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.scene-card {
		width: 100%;
		box-sizing: border-box;
	}
	.side {
		display: flex;
		flex-direction: column;
		gap: 12px;
		width: 260px;
	}
	.bottom {
		display: flex;
		gap: 16px;
		flex-wrap: wrap;
	}
	.gauges {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.trendcard {
		flex: 1;
		min-width: 320px;
	}
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		padding: 14px;
	}
	/* rail panel internals */
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
	.row b,
	.fp-row b {
		font-family: var(--mono);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
		color: var(--ink);
	}
	.side .card :global(.field) {
		margin-top: 8px;
	}
	.side input[type='range'] {
		width: 100%;
		accent-color: var(--s1);
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.statusline {
		margin: 8px 0 0;
		font-family: var(--mono);
		font-size: 0.8rem;
		color: var(--ink-2);
	}
	.btn {
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--ink);
		border-radius: var(--radius, 8px);
		padding: 0.55rem 0.9rem;
		font-size: 0.88rem;
		font-weight: 550;
		cursor: pointer;
		margin-top: 8px;
	}
	.btn:hover {
		border-color: var(--accent, var(--s1));
	}
	.btn.on {
		border-color: var(--good);
		color: var(--good);
	}
	.btn.ack:disabled {
		opacity: 0.45;
		cursor: default;
	}
	/* faceplate content layout */
	.fp-cols {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}
	.fp-panel {
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 12px;
		display: grid;
		gap: 10px;
		align-content: start;
		min-width: 0;
	}
	.fp-head {
		font-size: 10px;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--muted);
	}
	.fp-rows {
		display: grid;
		gap: 6px;
	}
	.fp-row {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		font-size: 12.5px;
		color: var(--ink-2);
	}
	.fp-pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.fp-center {
		display: grid;
		justify-items: center;
	}
	.fp-note {
		margin: 0;
		font-size: 11.5px;
		color: var(--muted);
	}
	.fp-tuning {
		display: flex;
		flex-wrap: wrap;
		gap: 10px;
	}
	@media (max-width: 480px) {
		.fp-cols {
			grid-template-columns: 1fr;
		}
	}
	@media (max-width: 720px) {
		.main-col {
			min-width: 0;
		}
		.side {
			width: 100%;
		}
	}
	.notank {
		background: var(--surface);
		border: 1px solid color-mix(in srgb, var(--warn) 45%, var(--border));
		border-radius: var(--radius, 8px);
		padding: 0.7rem 0.9rem;
		font-size: 0.85rem;
		color: var(--ink-2);
		margin-top: 12px;
	}
	.idle {
		opacity: 0.45;
	}
	.waiting {
		padding: 3rem 1rem;
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
</style>
