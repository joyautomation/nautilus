<script lang="ts">
	// Showcase for the legacy-port half of the kit: the pieces you reach for
	// when the screen already exists in Ignition, FactoryTalk or WinCC and the
	// job is to reproduce it, not to design it.
	//
	// Everything here runs off one animated value so the components move —
	// bands cross, the flow link starts marching, the status rows change
	// colour — which is the only way to see whether an indication actually
	// reads at a glance.
	import { onMount } from 'svelte';
	import {
		LevelTank,
		ScaleBar,
		Gauge,
		StatusRow,
		EquipSymbol,
		TankGlyph,
		PumpGlyph,
		ValveGlyph,
		FlowLink,
		CoordinateCanvas,
		WriteNumber,
		WriteToggle,
		CommandButton,
		AckButton,
		Button,
		EquipmentCard,
		FaceplateShell,
		Sparkline,
		StateChip,
		ValueText,
		confirm,
		type AlarmInstance,
		type CanvasSpec,
		type CanvasNode
	} from '@joyautomation/nautilus-hmi';

	// A stand-in symbol library. Real ports point `src` at the PNGs exported
	// from the gateway; the wrapper doesn't care what the picture is.
	const PUMP_PNG =
		'data:image/svg+xml;utf8,' +
		encodeURIComponent(
			`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 100">
				<rect x="10" y="62" width="100" height="30" rx="4" fill="#b0bec5" stroke="#455a64" stroke-width="3"/>
				<circle cx="60" cy="44" r="32" fill="#cfd8dc" stroke="#455a64" stroke-width="3"/>
				<path d="M60 44 L60 16 M60 44 L85 60 M60 44 L35 60" stroke="#455a64" stroke-width="4" fill="none"/>
			</svg>`
		);

	// ── one animated process value ────────────────────────────────────────
	let t = $state(0);
	onMount(() => {
		const id = setInterval(() => (t += 1), 700);
		return () => clearInterval(id);
	});

	/** Level in feet, 0…32, sweeping slowly through every alarm band. */
	const level = $derived(16 + 14 * Math.sin(t / 9));
	const flow = $derived(Math.max(0, 900 + 700 * Math.sin(t / 6)));
	const running = $derived(flow > 400);

	// Alarm limits, in feet. A DISABLED limit is null — that is the whole
	// convention: `l={LENB ? LSP : null}`.
	const LL = 4,
		L = 8,
		H = 26,
		HH = 30;

	const band = $derived(
		level >= HH || level <= LL ? 'crit' : level >= H || level <= L ? 'warn' : 'ok'
	);
	const bandFill = $derived(
		band === 'crit' ? 'var(--crit)' : band === 'warn' ? 'var(--warn)' : 'var(--s1)'
	);

	// ── a fake write transport ────────────────────────────────────────────
	// `write(name, value)` resolves to null on success or a refusal reason.
	// `RealtimeClient.writeTag` already has this shape; so does this.
	let sp = $state(24);
	let enabled = $state(true);
	let lastCommand = $state('—');

	async function write(name: string, value: number | boolean): Promise<string | null> {
		if (name.includes('.')) return `refused — ${name} is a struct member`;
		if (name === 'LevelSP') sp = value as number;
		if (name === 'Enable') enabled = value as boolean;
		if (value === true) lastCommand = `${name} pulsed at ${new Date().toLocaleTimeString()}`;
		return null;
	}

	// ── a tiny coordinate canvas ──────────────────────────────────────────
	// A transcriber emits specs like this one; the renderer scales the whole
	// 420×180 plane to whatever width it lands in. `registry` maps `t` to your
	// components; the `leaf` snippet below covers everything else.
	const spec: CanvasSpec = {
		width: 420,
		height: 180,
		items: [
			{ t: 'label', x: 12, y: 8, w: 200, h: 20, p: { text: 'CLEARWELL 1 — north bay' } },
			{ t: 'pipe', x: 96, y: 96, w: 150, h: 12 },
			{ t: 'pipe', x: 246, y: 40, w: 12, h: 68 },
			{ t: 'vessel', x: 12, y: 36, w: 84, h: 120, p: { tag: 'LIT_001' } },
			{ t: 'motor', x: 150, y: 60, w: 60, h: 60, p: { tag: 'SUP_015' } },
			{
				t: 'flex',
				x: 268,
				y: 40,
				w: 140,
				h: 120,
				p: { direction: 'column', justify: 'space-between' },
				c: [
					{ t: 'row', grow: 0, p: { label: 'P-1', state: 'run' } },
					{ t: 'row', grow: 0, p: { label: 'P-2', state: 'off' } },
					{ t: 'row', grow: 0, p: { label: 'P-3', state: 'bad' } }
				]
			}
		]
	};

	const prop = (n: CanvasNode, k: string) => String(n.p?.[k] ?? '');

	// ── quality, the fourth axis ──────────────────────────────────────────
	// A number, and the two facts about it. Everything below reads these the
	// same way, because `ValueText` is the only thing that renders a value.
	const history = $derived(Array.from({ length: 40 }, (_, i) => 16 + 14 * Math.sin((t - 40 + i) / 9)));

	// ── the faceplate ─────────────────────────────────────────────────────
	let openTag = $state<string | null>(null);
	let tab = $state(0);
	// `auto` is what an app ships; the two explicit values are here so both
	// hosts can be seen without resizing the window.
	let host = $state<'auto' | 'modal' | 'page'>('auto');
	// Derived here rather than inline: a snippet body does not keep the
	// narrowing that `{#if openTag}` establishes around it.
	const isSim = $derived(openTag?.endsWith('SUP_017') ?? false);
	const isMissing = $derived(openTag?.endsWith('SUP_018') ?? false);
	const TABS = ['Value', 'Scale', 'Alarms'];

	// ── alarms, for the confirm step ──────────────────────────────────────
	const alarms: AlarmInstance[] = [
		{ id: 'a1', tag: 'LIT_001', name: 'Clearwell 1 level high-high', priority: 'critical', state: 'unack-active', cond: true, activeMs: Date.now() - 240_000 },
		{ id: 'a2', tag: 'SUP_015', name: 'Supply pump 15 fault', priority: 'high', state: 'unack-active', cond: true, activeMs: Date.now() - 900_000 },
		{ id: 'a3', tag: 'FIT_004', name: 'Discharge flow low', priority: 'low', state: 'unack-active', cond: true, activeMs: Date.now() - 60_000 }
	];
	let acked = $state<string[]>([]);
	let lastConfirm = $state('—');

	async function askSomething() {
		const ok = await confirm({
			title: 'Reset SUP-015 runtime?',
			body: 'The accumulated hours go to zero and cannot be recovered.',
			confirmLabel: 'Reset',
			danger: true,
			operator: true,
			note: 'Recorded against the name above — unauthenticated.'
		});
		lastConfirm = ok ? `reset confirmed at ${new Date().toLocaleTimeString()}` : 'cancelled';
	}
</script>

<h1>Legacy-port components</h1>
<p class="lede">
	The kit's answer to "this screen already exists and has to come across". Every value below is one
	slow sine wave, so the indications actually change.
</p>

<section>
	<h2>Level tank &amp; banded scale</h2>
	<div class="row">
		<LevelTank
			value={level}
			min={0}
			max={32}
			units="ft"
			label="CLEARWELL 1"
			marks={[LL, L, H]}
			overflow={HH}
			volume={level * 41_800}
			fill={bandFill}
		/>
		<ScaleBar
			value={level}
			min={0}
			max={32}
			label="Level"
			units="ft"
			hh={HH}
			h={H}
			l={L}
			ll={LL}
			length={190}
			thickness={30}
		/>
		<ScaleBar
			value={flow}
			min={0}
			max={2000}
			label="Discharge"
			units="gpm"
			precision={0}
			h={1600}
			hh={1850}
			orientation="horizontal"
			length={220}
			thickness={22}
		/>
	</div>
	<p class="note">
		A disabled limit is <code>null</code>, not a separate enable flag — the low-low band on the
		flow bar is simply absent because that alarm is off.
	</p>
</section>

<section>
	<h2>Gauge: arc vs ring</h2>
	<div class="row">
		<Gauge value={level} min={0} max={32} unit="ft" label="default — 240°" setpoint={sp} />
		<Gauge value={level} min={0} max={32} unit="ft" label="gap={30}" gap={30} setpoint={sp} />
		<Gauge value={flow} min={0} max={2000} decimals={0} unit="gpm" label="sweep={180}" sweep={180} />
	</div>
</section>

<section>
	<h2>Image symbols</h2>
	<div class="row">
		<EquipSymbol
			src={PUMP_PNG}
			width={92}
			height={80}
			label="SUP-015"
			stateText={running ? 'On' : 'Off'}
			{running}
			auto={true}
			remote={true}
		/>
		<EquipSymbol
			src={PUMP_PNG}
			width={92}
			height={80}
			label="SUP-016"
			stateText="Off"
			auto={false}
			remote={false}
			fault={true}
		/>
		<EquipSymbol
			src={PUMP_PNG}
			width={92}
			height={80}
			label="SUP-017 (simulated)"
			stateText="On"
			running={true}
			simulate={true}
			mirror={true}
		/>
	</div>
	<p class="note">
		Pass both <code>width</code> and <code>height</code> and the picture is <code>contain</code>-fit
		into that box — legacy symbol PNGs have wildly different aspect ratios.
	</p>
</section>

<section>
	<h2>Status rows</h2>
	<div class="rows">
		<StatusRow state={running ? 'on' : 'off'} label="P-1" auto={true} remote={true} wide />
		<StatusRow state="off" label="P-2" auto={false} remote={true} wide />
		<StatusRow state="fault" label="P-3" auto={true} remote={false} wide />
		<StatusRow state="unknown" label="P-4" auto={null} remote={null} wide />
	</div>
	<p class="note">
		<code>unknown</code> is the honest fourth state: an absent point and a stopped pump mean
		opposite things, so they never share a colour.
	</p>
</section>

<section>
	<h2>Schematic glyphs</h2>
	<svg viewBox="0 0 460 170" class="schem" role="img" aria-label="Two tanks, a pump and a valve">
		<FlowLink d="M 118 96 L 208 96" flowing={running} />
		<FlowLink d="M 244 96 L 300 96 L 300 60" flowing={running} dashed />
		<FlowLink d="M 336 60 L 400 60" dead />
		<TankGlyph
			x={12}
			y={30}
			w={104}
			h={120}
			level={(level - 0) / 32}
			fill={bandFill}
			marks={[LL / 32, L / 32, H / 32, HH / 32]}
			id="CW-1"
			corner={`${Math.round((level / 32) * 100)}%`}
			value={`${level.toFixed(1)} / 32.0 ft`}
			sub={`${Math.round(level * 41_800).toLocaleString()} gal`}
		/>
		<PumpGlyph
			cx={226}
			cy={96}
			label="P1"
			value={running ? String(Math.round(flow)) : ''}
			{running}
		/>
		<ValveGlyph cx={318} cy={60} label="TV-4" value={running ? '1,180' : ''} open={running} />
		<PumpGlyph cx={420} cy={60} label="P2" nodata />
	</svg>
	<p class="note">
		<code>FlowLink</code> draws its marching dashes only while <code>flowing</code> — a still line
		means still water. The right-hand pump is <code>nodata</code>: no reading, so no state claimed.
	</p>
</section>

<section>
	<h2>Coordinate canvas</h2>
	<div class="canvas-frame">
		<CoordinateCanvas
			{spec}
			graphics={['label', 'pipe']}
			fontFamily="var(--font)"
			fontSize="13px"
			color="var(--ink)"
		>
			{#snippet leaf(node: CanvasNode)}
				{#if node.t === 'label'}
					<strong>{prop(node, 'text')}</strong>
				{:else if node.t === 'pipe'}
					<div class="pipe"></div>
				{:else if node.t === 'vessel'}
					<LevelTank
						value={level}
						min={0}
						max={32}
						units="ft"
						width={node.w ?? 80}
						height={(node.h ?? 100) - 12}
						fill={bandFill}
						showPercent={false}
					/>
				{:else if node.t === 'motor'}
					<EquipSymbol src={PUMP_PNG} width={54} height={44} showChips={false} {running} />
				{:else if node.t === 'row'}
					<StatusRow
						state={prop(node, 'state') === 'run'
							? running
								? 'on'
								: 'off'
							: prop(node, 'state') === 'bad'
								? 'unknown'
								: 'off'}
						label={prop(node, 'label')}
						wide
					/>
				{/if}
			{/snippet}
		</CoordinateCanvas>
	</div>
	<p class="note">
		The plane is 420×180 no matter how wide the page is — resize the window and everything scales
		together, which is what keeps a pipe attached to its pump. A real port passes a
		<code>registry</code> (node type → component) instead of the <code>leaf</code> snippet used here
		to keep the demo to one file.
	</p>
</section>

<section>
	<h2>Write-back controls</h2>
	<div class="row wrap">
		<WriteNumber tag="LevelSP" label="Level setpoint" value={sp} units="ft" {write} />
		<WriteNumber
			tag="LIT_001.HSP"
			label="High alarm (member)"
			value={H}
			units="ft"
			readonly
			readonlyReason="Read-only: this transport writes top-level tags only"
			{write}
		/>
		<div class="stack">
			<WriteToggle tag="Enable" label="Auto enable" value={enabled} {write} />
			<WriteToggle tag="HiTempAlm" label="High temperature" value={band === 'crit'} alarm {write} />
		</div>
		<div class="stack">
			<CommandButton tag="Start" label="Start" kind="start" {write} />
			<CommandButton tag="Stop" label="Stop" kind="stop" {write} />
			<CommandButton
				tag="SUP_015.RESET"
				label="Reset"
				disabled
				disabledReason="Unavailable: struct-member command"
				{write}
			/>
		</div>
	</div>
	<p class="note">Last command: <code>{lastCommand}</code></p>
</section>

<section>
	<h2>Quality-aware values</h2>
	<div class="row wrap">
		<ValueText label="Good" value={level} units="ft" size="lg" />
		<ValueText label="Stale" value={level} units="ft" size="lg" quality="stale" ageMs={735_000} />
		<ValueText label="Bad" value={level} units="ft" size="lg" quality="bad" />
		<ValueText label="Simulated" value={level} units="ft" size="lg" simulated />
		<ValueText label="Not published" value={level} units="ft" size="lg" present={false} />
	</div>
	<p class="note">
		One primitive, five states. The value is <strong>never blanked</strong> for stale or bad — it is
		what the plant last was, and an operator needs it plus its age far more than a dash. Only a point
		the runtime does not publish at all loses its number.
	</p>
</section>

<section>
	<h2>Equipment cards</h2>
	<div class="cards">
		<EquipmentCard
			label="SUP-015"
			description="Supply pump — north bay"
			tag="RTU9/SUP_015"
			src={PUMP_PNG}
			{running}
			auto={true}
			remote={true}
			stateText={running ? 'On' : 'Off'}
			values={[{ label: 'Discharge', value: flow, units: 'gpm', precision: 0 }]}
			chips={[{ label: running ? 'RUN' : 'STOP', kind: running ? 'good' : 'off' }, { label: 'AUTO' }]}
			onopen={() => ((openTag = 'RTU9/SUP_015'), (tab = 0))}
		>
			{#snippet sparkline()}
				<Sparkline values={history} height={28} />
			{/snippet}
		</EquipmentCard>

		<EquipmentCard
			label="LIT-001"
			description="Clearwell 1 level transmitter"
			tag="RTU9/LIT_001"
			src={PUMP_PNG}
			values={[
				{ label: 'Level', value: level, units: 'ft' },
				{ label: 'Volume', value: level * 41800, units: 'gal', precision: 0 }
			]}
			chips={[{ label: band === 'crit' ? 'HI-HI' : band === 'warn' ? 'HIGH' : 'NORMAL', kind: band === 'crit' ? 'critical' : band === 'warn' ? 'warning' : 'good' }]}
			fault={band === 'crit'}
			onopen={() => ((openTag = 'RTU9/LIT_001'), (tab = 0))}
		/>

		<EquipmentCard
			label="SUP-017"
			description="Supply pump — simulated value"
			tag="RTU9/SUP_017"
			src={PUMP_PNG}
			running
			simulated
			stateText="On"
			values={[{ label: 'Discharge', value: 1180, units: 'gpm', precision: 0 }]}
			chips={[{ label: 'RUN', kind: 'good' }]}
			onopen={() => ((openTag = 'RTU9/SUP_017'), (tab = 0))}
		/>

		<EquipmentCard
			label="SUP-018"
			description="Supply pump — not published by this runtime"
			tag="RTU9/SUP_018"
			src={PUMP_PNG}
			present={false}
			values={[{ label: 'Discharge', units: 'gpm' }]}
			onopen={() => ((openTag = 'RTU9/SUP_018'), (tab = 0))}
		/>
	</div>
	<p class="note">
		Quality drives the border: solid <code>--q-notpublished</code> for a point the runtime does not
		publish, dashed <code>--q-simulated</code> for a substituted value. The whole card is the tap
		target — tap one to open the faceplate.
	</p>
</section>

<section>
	<h2>Faceplate shell</h2>
	<div class="row wrap">
		<Button variant="primary" onclick={() => ((host = 'auto'), (openTag = 'RTU9/LIT_001'), (tab = 0))}>
			Open LIT-001
		</Button>
		<Button onclick={() => ((host = 'page'), (openTag = 'RTU9/LIT_001'), (tab = 0))}>
			…as a full page
		</Button>
		<StateChip label="auto: modal ≥ 900px · full page below" kind="neutral" dot={false} />
	</div>
	<p class="note">
		One layout for every equipment family, and <strong>two hosts from one prop</strong>: narrow the
		window below 900px and the same faceplate renders as a full page instead of a floating modal.
		The second button forces <code>as="page"</code> — it renders in place at the bottom of this
		page, because in a real app the page host is mounted on its own route.
	</p>
</section>

<section>
	<h2>Confirm before you command</h2>
	<div class="row wrap">
		<AckButton
			alarms={alarms.filter((a) => !acked.includes(a.id))}
			onack={(ids) => (acked = [...acked, ...ids])}
			label="Ack 3 alarms"
			variant="primary"
		/>
		<AckButton
			alarms={[alarms[1]]}
			onack={(ids) => (acked = [...acked, ...ids])}
			label="Ack one row"
		/>
		<Button variant="danger" onclick={askSomething}>Reset runtime</Button>
	</div>
	<p class="note">
		Ack All enumerates worst first; <strong>a single row confirms too</strong> — there is no role
		gate, and the record is unauthenticated and permanent. Escape and the backdrop cancel, and Cancel
		takes focus, so a stray Enter cannot command the plant.
		<br />Acknowledged: <code>{acked.length ? acked.join(', ') : '—'}</code> · last confirm:
		<code>{lastConfirm}</code>
	</p>
</section>

{#if openTag}
	<FaceplateShell
		as={host}
		label={openTag.split('/').pop() ?? openTag}
		tag={openTag}
		typeName="Analog Input"
		simulated={isSim}
		present={!isMissing}
		tabs={TABS}
		bind:active={tab}
		chips={[
			{ label: running ? 'RUN' : 'STOP', kind: running ? 'good' : 'off' },
			{ label: 'AUTO' },
			{ label: 'REMOTE' },
			{ label: band === 'crit' ? 'HI-HI ALARM' : 'NO ALARM', kind: band === 'crit' ? 'critical' : 'good' }
		]}
		onclose={() => (openTag = null)}
	>
		{#snippet hero()}
			<div class="heroscale">
				<ScaleBar value={level} min={0} max={32} label="Level" units="ft" hh={HH} h={H} l={L} ll={LL} length={150} thickness={26} />
			</div>
			<div class="herotrend">
				<Sparkline values={history} height={72} />
				<ValueText label="Level" value={level} units="ft" size="lg" present={!isMissing} simulated={isSim} />
			</div>
		{/snippet}

		{#snippet panel(label: string)}
			{#if label === 'Value'}
				<Gauge value={level} min={0} max={32} unit="ft" label="Level" setpoint={sp} />
			{:else if label === 'Scale'}
				<div class="row wrap">
					<WriteNumber tag="LIT_001.HHSP" label="High-high" value={HH} units="ft" {write} />
					<WriteNumber tag="LIT_001.HSP" label="High" value={H} units="ft" {write} />
					<WriteNumber tag="LIT_001.LSP" label="Low" value={L} units="ft" {write} />
					<WriteNumber tag="LIT_001.LLSP" label="Low-low" value={LL} units="ft" {write} />
				</div>
				<p class="note">Setpoints commit on blur/Enter and do <strong>not</strong> confirm — they are adjustments, not commands.</p>
			{:else}
				<div class="rows">
					<StatusRow state={band === 'crit' ? 'fault' : 'off'} label="High-high" wide />
					<StatusRow state={band === 'warn' ? 'fault' : 'off'} label="High" wide />
					<StatusRow state="off" label="Low" wide />
				</div>
			{/if}
		{/snippet}

		{#snippet sim()}
			<div class="row wrap">
				<WriteToggle tag="LIT_001.SIMULATE" label="Simulate" value={isSim} {write} />
				<WriteNumber tag="LIT_001.SIMVALUE" label="Simulated value" value={level} units="ft" {write} />
			</div>
			<p class="note">
				The simulated value substitutes <strong>before</strong> alarming, in the controller — every
				indication and every alarm downstream sees it. A production feature of every analog block,
				on every build.
			</p>
		{/snippet}

		{#snippet footer(writable: boolean)}
			<CommandButton tag="LIT_001.RESET" label="Reset" disabled={!writable} disabledReason="Disabled — the runtime cannot vouch for this value" {write} />
			<Button variant="primary" disabled={!writable} onclick={askSomething}>Command…</Button>
		{/snippet}
	</FaceplateShell>
{/if}

<style>
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(320px, 100%), 1fr));
		gap: var(--space-3);
	}

	.heroscale {
		flex: none;
		width: 150px;
	}

	.herotrend {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		min-width: 200px;
		flex: 1;
	}

	.lede {
		max-width: 62ch;
	}

	section {
		margin: 28px 0;
	}

	.row {
		display: flex;
		gap: 26px;
		align-items: flex-end;
		flex-wrap: wrap;
	}

	.row.wrap {
		align-items: flex-start;
	}

	.stack {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 180px;
	}

	.rows {
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-width: 320px;
	}

	.schem {
		width: 100%;
		max-width: 560px;
		height: auto;
		display: block;
		font-family: var(--font);
	}

	.canvas-frame {
		max-width: 560px;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		padding: 4px;
		overflow: hidden;
	}

	.pipe {
		width: 100%;
		height: 100%;
		border-radius: 2px;
		background: linear-gradient(to bottom, var(--axis), var(--surface-2), var(--axis));
	}

	.note {
		color: var(--muted);
		font-size: var(--font-2xs);
		max-width: 68ch;
		margin-top: 10px;
	}

	code {
		font-family: var(--mono);
	}
</style>
