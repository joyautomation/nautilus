<script lang="ts">
	// The panel: the kind's fields, each committed per change (blur / Enter)
	// as a setField op; its drives as rows (DriveRow); and the sample
	// values that drive the stage — kept here, never written to the file.
	import { formatStatus, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import DriveRow from './DriveRow.svelte';
	import { ancestorsOf, blankDrive, drivesOn, kd, memberShape, members, postOp, sampleStruct } from './kindState.svelte';

	const kind = $derived(kd.kind);
	const memberList = $derived(members());
	const statusPreview = $derived(kind?.status ? formatStatus(kind.status, sampleStruct(), true) : '');

	function text(e: Event, key: 'type' | 'status' | 'model') {
		const v = (e.currentTarget as HTMLInputElement).value.trim();
		postOp({ type: 'setField', key, value: v === '' ? null : v });
	}
	function membersField(e: Event) {
		const v = (e.currentTarget as HTMLInputElement).value
			.split(/[,\s]+/)
			.map((m) => m.trim())
			.filter(Boolean);
		postOp({ type: 'setField', key: 'members', value: v.length ? v : null });
	}
	const AXES = ['x', 'y', 'z'] as const;
	const fmt = (v: number | undefined) => (v === undefined ? '' : String(Math.round(v * 1000) / 1000));
	function labelAt(e: Event, i: number) {
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const cur: Vec3 = [...(kind?.labelAt ?? [0, 0, 0])] as Vec3;
		if (raw === '' && !kind?.labelAt) return;
		const v = raw === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		cur[i] = v;
		postOp({ type: 'setField', key: 'labelAt', value: cur });
	}
	function clearLabelAt() {
		postOp({ type: 'setField', key: 'labelAt', value: null });
	}
	const boundsMode = $derived(kind?.bounds === undefined ? 'auto' : kind.bounds === 'auto' ? 'auto' : 'box');
	function setBoundsMode(e: Event) {
		const m = (e.currentTarget as HTMLSelectElement).value;
		if (m === 'auto') postOp({ type: 'setField', key: 'bounds', value: null });
		else postOp({ type: 'setField', key: 'bounds', value: kd.bounds ?? { size: [0.4, 0.4, 0.4], center: [0, 0.2, 0] } });
	}
	function boundsVec(e: Event, key: 'size' | 'center', i: number) {
		const b = kind?.bounds;
		if (!b || b === 'auto') return;
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const v = raw === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		const next = { size: [...b.size] as Vec3, center: [...b.center] as Vec3 };
		next[key][i] = v;
		postOp({ type: 'setField', key: 'bounds', value: next });
	}
	/** A new drive on the picked part, else the model's first object. */
	function addDrive() {
		const mesh = kd.picked ?? kd.meshes[0] ?? 'Mesh';
		postOp({ type: 'addDrive', drive: blankDrive(mesh, kd.meshes.length ? 'tint' : 'visible') });
	}
	function pick(m: string | null) {
		kd.picked = m;
	}
	function hover(m: string | null) {
		if (m === null ? kd.hover !== null : kd.hover !== m) kd.hover = m;
	}
	function sampleNum(e: Event, m: string) {
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const v = Number(raw);
		if (raw === '' || !isFinite(v)) delete kd.samples[m];
		else kd.samples[m] = v;
	}
	function sampleBool(e: Event, m: string) {
		kd.samples[m] = (e.currentTarget as HTMLInputElement).checked;
	}
	const sample = (m: string) => sampleStruct()[m];
</script>

<aside aria-label="Kind">
	{#if kind}
		<div class="head">Kind</div>
		<div class="fact">
			<span>name</span>
			<code class="ro" title="The file's name, minus .kind.json — what a scene's kinds entry calls it">{kd.name}</code>
		</div>
		<label>
			<span>type</span>
			<input class="nx-input" aria-label="type" value={kind.type ?? ''} onchange={(e) => text(e, 'type')} placeholder="UDT name" spellcheck="false" title="The UDT a node of this kind binds" />
		</label>
		<label>
			<span>members</span>
			<input class="nx-input" aria-label="members" value={(kind.members ?? []).join(', ')} onchange={membersField} placeholder="beyond the drives'" spellcheck="false" title="Members the kind reads that no drive or status names" />
		</label>
		<label>
			<span>model</span>
			<input class="nx-input" aria-label="model" value={kind.model ?? ''} onchange={(e) => text(e, 'model')} placeholder="models/pump.glb" spellcheck="false" title="A .glb as a URL path from the app root (hmi/static/)" class:bad={kd.model && !kd.model.file} />
		</label>
		{#if kd.model && !kd.model.file}
			<p class="note bad">Not found: looked next to this file and up through hmi/static/, hmi/build/.</p>
		{:else if kd.model?.file}
			<p class="note" title={kd.model.file}>{kd.model.file.split('/').slice(-3).join('/')}</p>
		{/if}
		<label>
			<span>status</span>
			<input class="nx-input" aria-label="status" value={kind.status ?? ''} onchange={(e) => text(e, 'status')} placeholder="{'{'}Level:1{'}'} %" spellcheck="false" title="The label's value text as a template over members" />
		</label>
		{#if kind.status}
			<p class="note">reads <b data-kind="status-preview">{statusPreview}</b> with the samples below</p>
		{/if}
		<div class="vec">
			<span>label at <small>m</small></span>
			{#each AXES as a, i (a)}
				<input class="nx-input" type="number" step="0.01" value={fmt(kind.labelAt?.[i])} onchange={(e) => labelAt(e, i)} aria-label="labelAt {a}" title={a} placeholder="top" />
			{/each}
		</div>
		{#if kind.labelAt}<button class="link" onclick={clearLabelAt}>label at the top of the bounds</button>{/if}
		<label>
			<span>bounds</span>
			<select class="nx-input" value={boundsMode} onchange={setBoundsMode} title="The halo and selection box">
				<option value="auto">auto (the model's box)</option>
				<option value="box">a box</option>
			</select>
		</label>
		{#if kind.bounds && kind.bounds !== 'auto'}
			<div class="vec">
				<span>size</span>
				{#each AXES as a, i (a)}<input class="nx-input" type="number" step="0.01" value={fmt(kind.bounds.size[i])} onchange={(e) => boundsVec(e, 'size', i)} aria-label="size {a}" title={a} />{/each}
			</div>
			<div class="vec">
				<span>center</span>
				{#each AXES as a, i (a)}<input class="nx-input" type="number" step="0.01" value={fmt(kind.bounds.center[i])} onchange={(e) => boundsVec(e, 'center', i)} aria-label="center {a}" title={a} />{/each}
			</div>
		{/if}

		{#if kd.modelState === 'ok'}
			<div class="head sub">Parts <span class="count">{kd.meshes.length}</span></div>
			{#if kd.picked}
				<p class="note part" data-kind="picked">
					<b>{kd.picked}</b>
					{#each ancestorsOf(kd.picked) as a (a)}
						<span class="in">· in <button class="link" onclick={() => pick(a)} title="Pick the part this one is part of">{a}</button></span>
					{/each}
					· {drivesOn(kd.picked)} drive{drivesOn(kd.picked) === 1 ? '' : 's'}
					· <button class="link" onclick={() => pick(null)} title="Nothing picked (Esc)">clear</button>
				</p>
			{:else}
				<p class="note">Click a part on the model, or a name here — a new drive starts on it.</p>
			{/if}
			<div class="chips" role="listbox" aria-label="Parts">
				{#each kd.meshes as m (m)}
					<button
						class="chip"
						class:picked={kd.picked === m}
						class:hover={kd.hover === m}
						class:driven={drivesOn(m) > 0}
						role="option"
						aria-selected={kd.picked === m}
						data-kind="part"
						data-name={m}
						onclick={() => pick(kd.picked === m ? null : m)}
						onpointerenter={() => hover(m)}
						onpointerleave={() => kd.hover === m && hover(null)}
						title={drivesOn(m) ? `${m} · ${drivesOn(m)} drive${drivesOn(m) === 1 ? '' : 's'}` : m}
					>
						{m}{#if drivesOn(m)}<small>{drivesOn(m)}</small>{/if}
					</button>
				{/each}
			</div>
		{/if}

		<div class="head sub">Drives <span class="count">{kind.drive?.length ?? 0}</span></div>
		{#each kind.drive ?? [] as d, i (i + ':' + d.mesh)}
			<DriveRow drive={d} index={i} />
		{/each}
		<div class="actions">
			<button onclick={addDrive} title={kd.picked ? `A new drive on ${kd.picked} — pick the channel in its row` : "A new drive on the model's first object — pick the mesh and channel in its row"}>
				Add drive{#if kd.picked} on <code>{kd.picked}</code>{/if}
			</button>
		</div>

		<div class="head sub">Sample values</div>
		{#if memberList.length === 0}
			<p class="note">No members yet — a drive or a status template names them.</p>
		{/if}
		{#each memberList as m (m)}
			<label class="sample">
				<span><code>{m}</code></span>
				{#if memberShape(m) === 'bool'}
					<input type="checkbox" checked={sample(m) === true} onchange={(e) => sampleBool(e, m)} aria-label="sample {m}" />
				{:else}
					<input class="nx-input" type="number" step="any" value={String(sample(m))} onchange={(e) => sampleNum(e, m)} aria-label="sample {m}" />
				{/if}
			</label>
		{/each}
	{/if}
</aside>

<datalist id="kind-members">
	{#each memberList as m (m)}<option value={m}></option>{/each}
</datalist>

<style>
	aside {
		width: 300px;
		flex: none;
		overflow-y: auto;
		border-left: 1px solid var(--nx-border);
		padding: 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 12px;
		color: var(--nx-ui-ink);
	}
	.head {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--nx-muted);
		padding-bottom: 2px;
	}
	.head.sub {
		margin-top: 8px;
	}
	.count {
		opacity: 0.7;
	}
	label,
	.fact,
	.vec {
		display: grid;
		grid-template-columns: 58px 1fr;
		align-items: center;
		gap: 4px;
	}
	.vec {
		grid-template-columns: 58px 1fr 1fr 1fr;
	}
	label.sample {
		grid-template-columns: 1fr 90px;
	}
	label span,
	.fact span,
	.vec span {
		color: var(--nx-muted);
	}
	small {
		opacity: 0.7;
	}
	code,
	.ro {
		font-family: var(--nx-mono);
	}
	.nx-input {
		width: 100%;
		min-width: 0;
		box-sizing: border-box;
		font-size: 12px;
		padding: 3px 5px;
	}
	.nx-input.bad {
		border-color: var(--nx-err);
	}
	input[type='number'] {
		font-family: var(--nx-mono);
	}
	.note {
		margin: 0;
		color: var(--nx-muted);
		line-height: 1.4;
		overflow-wrap: anywhere;
	}
	.note.bad {
		color: var(--nx-err);
	}
	.note b {
		color: var(--nx-ui-ink);
		font-family: var(--nx-mono);
		font-weight: 600;
	}
	.link {
		border: 0;
		background: none;
		color: var(--nx-accent);
		cursor: pointer;
		font: inherit;
		font-size: 11px;
		text-align: left;
		padding: 0;
	}
	.actions button {
		width: 100%;
		padding: 5px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
	}
	.actions button:hover {
		background: var(--nx-hover);
	}
	.actions button code {
		font-family: var(--nx-mono);
	}
	.note.part b {
		font-family: var(--nx-mono);
		color: var(--nx-ui-ink);
	}
	.chips {
		/* a scrolling flex item of the aside's column would shrink to nothing */
		flex: none;
		display: flex;
		flex-wrap: wrap;
		gap: 3px;
		max-height: 132px;
		overflow-y: auto;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 6px;
		border: 1px solid var(--nx-border);
		border-radius: 10px;
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-family: var(--nx-mono);
		font-size: 11px;
		line-height: 1.5;
	}
	.chip small {
		font-size: 9px;
		opacity: 0.7;
	}
	.chip.driven {
		border-color: var(--nx-muted);
	}
	.chip.hover,
	.chip:hover {
		background: var(--nx-hover);
	}
	.chip.picked {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
		border-color: var(--nx-sel-bg);
	}
</style>
