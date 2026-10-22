<script lang="ts">
	// One drive: a mesh (from the model's names), a channel, and the
	// channel's few fields. Every change posts the WHOLE drive back as one
	// updateDrive, so a row is a row in the file too.
	import type { BoolExpr, Drive } from '@joyautomation/nautilus-hmi-3d';
	import { blankDrive, channelOf, kd, members, postOp } from './kindState.svelte';

	let { drive, index }: { drive: Drive; index: number } = $props();

	const CHANNELS = ['spin', 'turn', 'scale', 'tint', 'emissive', 'visible'] as const;
	const ch = $derived(channelOf(drive));
	const inModel = $derived(kd.modelState !== 'ok' || kd.meshes.includes(drive.mesh));
	const num = $derived.by(() => ('spin' in drive ? drive.spin.revPerS : 'turn' in drive ? drive.turn.deg : 'scale' in drive ? drive.scale.to : null));
	/** The ramp body, when this tint / emissive varies with a number. */
	const ramp = $derived.by(() => ('tint' in drive && 'ramp' in drive.tint ? drive.tint : 'emissive' in drive && 'ramp' in drive.emissive ? drive.emissive : null));
	const bool = $derived.by((): BoolExpr | null => {
		if ('tint' in drive) return 'ramp' in drive.tint ? null : drive.tint;
		if ('emissive' in drive) return 'ramp' in drive.emissive ? null : drive.emissive;
		if ('visible' in drive) return drive.visible;
		return null;
	});
	const axis = $derived('spin' in drive ? drive.spin.axis : 'turn' in drive ? drive.turn.axis : 'scale' in drive ? drive.scale.axis : '');

	function post(d: Drive) {
		postOp({ type: 'updateDrive', index, drive: d });
	}
	function clone(): Drive {
		return JSON.parse(JSON.stringify(drive)) as Drive;
	}
	function setMesh(e: Event) {
		const d = clone();
		d.mesh = (e.currentTarget as HTMLSelectElement | HTMLInputElement).value;
		post(d);
	}
	function setChannel(e: Event) {
		const c = (e.currentTarget as HTMLSelectElement).value as (typeof CHANNELS)[number];
		const bind = num?.bind ?? bool?.bind ?? members()[0] ?? 'Running';
		post(blankDrive(drive.mesh, c, bind.replace(/^!/, '')));
	}
	function setAxis(e: Event) {
		const d = clone();
		const a = (e.currentTarget as HTMLSelectElement).value;
		if ('spin' in d) d.spin.axis = a as 'x' | 'y' | 'z';
		else if ('turn' in d) d.turn.axis = a as 'x' | 'y' | 'z';
		else if ('scale' in d) d.scale.axis = a as 'x' | 'y' | 'z' | 'xyz';
		post(d);
	}
	/** A number field of the Num expression; blank deletes the optional ones. */
	function setNum(e: Event, key: 'scale' | 'offset' | 'min' | 'max') {
		const d = clone();
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const n = 'spin' in d ? d.spin.revPerS : 'turn' in d ? d.turn.deg : 'scale' in d ? d.scale.to : null;
		if (!n) return;
		if (raw === '') delete n[key];
		else {
			const v = Number(raw);
			if (!isFinite(v)) return;
			n[key] = v;
		}
		post(d);
	}
	function setBind(e: Event) {
		const d = clone();
		const v = (e.currentTarget as HTMLInputElement).value.trim();
		if (v === '') return;
		if ('spin' in d) d.spin.revPerS.bind = v;
		else if ('turn' in d) d.turn.deg.bind = v;
		else if ('scale' in d) d.scale.to.bind = v;
		else if ('tint' in d) d.tint.bind = v;
		else if ('emissive' in d) d.emissive.bind = v;
		else d.visible.bind = v;
		post(d);
	}
	function setText(e: Event, key: 'on' | 'off') {
		const d = clone();
		const v = (e.currentTarget as HTMLInputElement).value.trim();
		if ('tint' in d && !('ramp' in d.tint)) {
			if (key === 'off' && v === '') delete d.tint.off;
			else if (v !== '') d.tint[key] = v;
		} else if ('emissive' in d && !('ramp' in d.emissive) && key === 'on' && v !== '') d.emissive.on = v;
		post(d);
	}
	function setOptNum(e: Event, key: 'threshold' | 'intensity') {
		const d = clone();
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const b = 'tint' in d ? d.tint : 'emissive' in d ? d.emissive : 'visible' in d ? d.visible : null;
		if (!b) return;
		const o = b as unknown as Record<string, unknown>;
		if (raw === '') delete o[key];
		else {
			const v = Number(raw);
			if (!isFinite(v)) return;
			o[key] = v;
		}
		post(d);
	}
	function del() {
		postOp({ type: 'deleteDrive', index });
	}
	// ── a colour over a number ──
	function toggleRamp(e: Event) {
		const on = (e.currentTarget as HTMLInputElement).checked;
		const d = clone();
		const bind = (ramp?.bind ?? bool?.bind ?? members()[0] ?? 'Level').replace(/^!/, '');
		if ('tint' in d) d.tint = on ? { bind, ramp: [[0, 'stale'], [100, 'running']] } : { bind, on: 'running' };
		else if ('emissive' in d) d.emissive = on ? { bind, ramp: [[0, 'stale'], [100, 'running']], intensity: d.emissive.intensity } : { bind, on: 'running', intensity: d.emissive.intensity };
		post(d);
	}
	function rampOf(d: Drive): [number, string][] | null {
		return 'tint' in d && 'ramp' in d.tint ? d.tint.ramp : 'emissive' in d && 'ramp' in d.emissive ? d.emissive.ramp : null;
	}
	function setStop(e: Event, i: number, key: 0 | 1) {
		const d = clone();
		const r = rampOf(d);
		if (!r) return;
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		if (key === 0) {
			const v = Number(raw);
			if (raw === '' || !isFinite(v)) return;
			r[i][0] = v;
			r.sort((a, b) => a[0] - b[0]);
		} else {
			if (raw === '') return;
			r[i][1] = raw;
		}
		post(d);
	}
	function addStop() {
		const d = clone();
		const r = rampOf(d);
		if (!r) return;
		const last = r[r.length - 1];
		r.push([last[0] + 10, 'critical']);
		post(d);
	}
	function delStop(i: number) {
		const d = clone();
		const r = rampOf(d);
		if (!r || r.length <= 2) return;
		r.splice(i, 1);
		post(d);
	}
	const fmt = (v: number | undefined) => (v === undefined ? '' : String(v));
</script>

<div
	class="drive"
	class:picked={kd.picked === drive.mesh}
	role="group"
	aria-label="drive {index + 1} on {drive.mesh}"
	data-kind="drive-row"
	data-index={index}
	onpointerenter={() => kd.modelState === 'ok' && (kd.hover = drive.mesh)}
	onpointerleave={() => kd.hover === drive.mesh && (kd.hover = null)}
>
	<div class="row">
		{#if kd.modelState === 'ok'}
			<select class="nx-input mesh" value={drive.mesh} onchange={setMesh} aria-label="mesh" title={inModel ? 'The object in the model this drive moves' : 'Not in the model'} class:bad={!inModel}>
				{#if !inModel}<option value={drive.mesh}>{drive.mesh} (not in model)</option>{/if}
				{#each kd.meshes as m (m)}<option value={m}>{m}</option>{/each}
			</select>
		{:else}
			<input class="nx-input mesh" value={drive.mesh} onchange={setMesh} aria-label="mesh" placeholder="mesh name" spellcheck="false" />
		{/if}
		<select class="nx-input channel" value={ch} onchange={setChannel} aria-label="channel" title="What the value does to the mesh">
			{#each CHANNELS as c (c)}<option value={c}>{c}</option>{/each}
		</select>
		<button class="del" onclick={del} title="Remove this drive" aria-label="remove drive">×</button>
	</div>
	<div class="row fields">
		{#if num}
			<select class="nx-input axis" value={axis} onchange={setAxis} aria-label="axis" title="Local axis">
				<option value="x">x</option><option value="y">y</option><option value="z">z</option>
				{#if 'scale' in drive}<option value="xyz">xyz</option>{/if}
			</select>
			<input class="nx-input bind" value={num.bind} onchange={setBind} list="kind-members" aria-label="bind" placeholder="member" spellcheck="false" title="The member read" />
			<label title="value × scale"><span>×</span><input class="nx-input" type="number" step="any" value={fmt(num.scale)} onchange={(e) => setNum(e, 'scale')} aria-label="scale" placeholder="1" /></label>
			<label title="+ offset"><span>+</span><input class="nx-input" type="number" step="any" value={fmt(num.offset)} onchange={(e) => setNum(e, 'offset')} aria-label="offset" placeholder="0" /></label>
			<label title="clamp from"><span>≥</span><input class="nx-input" type="number" step="any" value={fmt(num.min)} onchange={(e) => setNum(e, 'min')} aria-label="min" placeholder="" /></label>
			<label title="clamp to"><span>≤</span><input class="nx-input" type="number" step="any" value={fmt(num.max)} onchange={(e) => setNum(e, 'max')} aria-label="max" placeholder="" /></label>
		{:else if ramp}
			<input class="nx-input bind" value={ramp.bind} onchange={setBind} list="kind-members" aria-label="bind" placeholder="member" spellcheck="false" title="The member read, a number" />
			<label class="toggle" title="The colour varies with the number: stops of value → colour"><input type="checkbox" checked onchange={toggleRamp} aria-label="ramp" /> ramp</label>
			{#if 'emissive' in drive}
				<label title="glow strength"><span>×</span><input class="nx-input" type="number" step="any" value={fmt(drive.emissive.intensity)} onchange={(e) => setOptNum(e, 'intensity')} aria-label="intensity" placeholder="1" /></label>
			{/if}
		{:else if bool}
			<input class="nx-input bind" value={bool.bind} onchange={setBind} list="kind-members" aria-label="bind" placeholder="member (! negates)" spellcheck="false" title="The member read; a leading ! negates" />
			<label title="true above this (a number)"><span>&gt;</span><input class="nx-input" type="number" step="any" value={fmt(bool.threshold)} onchange={(e) => setOptNum(e, 'threshold')} aria-label="threshold" placeholder="0" /></label>
			{#if 'tint' in drive && !('ramp' in drive.tint)}
				<label title="palette slot or CSS colour while true"><span>on</span><input class="nx-input" value={drive.tint.on} onchange={(e) => setText(e, 'on')} aria-label="on" spellcheck="false" /></label>
				<label title="while false (blank = the model's own)"><span>off</span><input class="nx-input" value={drive.tint.off ?? ''} onchange={(e) => setText(e, 'off')} aria-label="off" spellcheck="false" placeholder="own" /></label>
				<label class="toggle" title="Instead: the colour varies with a number"><input type="checkbox" onchange={toggleRamp} aria-label="ramp" /> ramp</label>
			{:else if 'emissive' in drive && !('ramp' in drive.emissive)}
				<label title="palette slot or CSS colour while true"><span>on</span><input class="nx-input" value={drive.emissive.on} onchange={(e) => setText(e, 'on')} aria-label="on" spellcheck="false" /></label>
				<label title="glow strength"><span>×</span><input class="nx-input" type="number" step="any" value={fmt(drive.emissive.intensity)} onchange={(e) => setOptNum(e, 'intensity')} aria-label="intensity" placeholder="1" /></label>
				<label class="toggle" title="Instead: the glow varies with a number"><input type="checkbox" onchange={toggleRamp} aria-label="ramp" /> ramp</label>
			{/if}
		{/if}
	</div>
	{#if ramp}
		<div class="stops" data-kind="ramp">
			{#each ramp.ramp as stop, i (i)}
				<div class="stop" data-kind="ramp-stop" data-index={i}>
					<input class="nx-input val" type="number" step="any" value={String(stop[0])} onchange={(e) => setStop(e, i, 0)} aria-label="stop value" title="At this value…" />
					<span class="arrow">→</span>
					<input class="nx-input col" value={stop[1]} onchange={(e) => setStop(e, i, 1)} aria-label="stop colour" spellcheck="false" title="…this colour: a palette slot or a CSS colour" />
					<span class="swatch" style:background={/^#|^rgb|^hsl/.test(stop[1]) ? stop[1] : 'transparent'} title={stop[1]}></span>
					<button class="del" onclick={() => delStop(i)} disabled={ramp.ramp.length <= 2} title="Remove this stop" aria-label="remove stop">×</button>
				</div>
			{/each}
			<button class="link" onclick={addStop} title="Another stop after the last">+ stop</button>
		</div>
	{/if}
</div>

<style>
	.drive {
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		padding: 4px 6px;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.drive.picked {
		border-color: var(--nx-accent);
	}
	.row {
		display: flex;
		gap: 4px;
		align-items: center;
	}
	.fields {
		flex-wrap: wrap;
	}
	.mesh {
		flex: 1;
		min-width: 0;
	}
	.mesh.bad {
		border-color: var(--nx-err);
		color: var(--nx-err);
	}
	.channel {
		width: 82px;
	}
	.axis {
		width: 48px;
	}
	.bind {
		width: 92px;
	}
	.fields label {
		display: inline-flex;
		align-items: center;
		gap: 2px;
	}
	.fields label span {
		color: var(--nx-muted);
		font-size: 11px;
	}
	.fields label input {
		width: 52px;
	}
	.nx-input {
		box-sizing: border-box;
		font-size: 12px;
		padding: 2px 4px;
	}
	input[type='number'],
	.bind {
		font-family: var(--nx-mono);
	}
	.toggle {
		font-size: 11px;
		color: var(--nx-muted);
		gap: 3px;
	}
	.stops {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding-left: 6px;
	}
	.stop {
		display: flex;
		align-items: center;
		gap: 3px;
	}
	.stop .val {
		width: 58px;
	}
	.stop .col {
		flex: 1;
		min-width: 0;
		font-family: var(--nx-mono);
	}
	.stop .arrow {
		color: var(--nx-muted);
		font-size: 11px;
	}
	.swatch {
		width: 12px;
		height: 12px;
		border-radius: 3px;
		border: 1px solid var(--nx-border);
		flex: none;
	}
	.stops .link {
		border: 0;
		background: none;
		color: var(--nx-accent);
		cursor: pointer;
		font: inherit;
		font-size: 11px;
		text-align: left;
		padding: 0;
	}
	.del {
		border: 1px solid transparent;
		border-radius: 4px;
		background: transparent;
		color: var(--nx-muted);
		cursor: pointer;
		font: inherit;
		font-size: 14px;
		line-height: 1;
		padding: 2px 6px;
	}
	.del:hover {
		color: var(--nx-err);
		background: var(--nx-hover);
	}
</style>
