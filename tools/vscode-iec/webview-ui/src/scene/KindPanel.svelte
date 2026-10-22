<script lang="ts">
	// The panel: the kind's fields, each committed per change (blur / Enter)
	// as a setField op; its drives as rows (DriveRow); and the sample
	// values that drive the stage — kept here, never written to the file.
	import { formatStatus, type ScenePoint, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import DriveRow from './DriveRow.svelte';
	import PointFields from './PointFields.svelte';
	import { ancestorsOf, blankDrive, drivesOn, kd, memberShape, members, postOp, sampleStruct } from './kindState.svelte';
	import { portNameFrom } from './marks';

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
	function setLabelAt(p: ScenePoint | string) {
		postOp({ type: 'setField', key: 'labelAt', value: p });
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
	// ── ports ──
	const ports = $derived(Object.entries(kind?.ports ?? {}));
	/** A port where the last click landed, named after the picked part. */
	function addPortHere() {
		if (!kd.hit) return;
		postOp({ type: 'setPort', name: portNameFrom(kd.picked, Object.keys(kind?.ports ?? {})), port: { at: kd.hit.at, dir: kd.hit.dir } });
	}
	function movePortHere(name: string) {
		if (!kd.hit) return;
		postOp({ type: 'setPort', name, port: { at: kd.hit.at, dir: kd.hit.dir } });
	}
	function renamePort(e: Event, name: string) {
		const to = (e.currentTarget as HTMLInputElement).value.trim();
		if (to === '' || to === name) {
			(e.currentTarget as HTMLInputElement).value = name;
			return;
		}
		postOp({ type: 'renamePort', name, to });
	}
	function portAt(name: string, at: ScenePoint | string) {
		const port = kind?.ports?.[name];
		if (!port || typeof at === 'string') return;
		postOp({ type: 'setPort', name, port: { ...port, at } });
	}
	function portDir(e: Event, name: string, i: number) {
		const port = kind?.ports?.[name];
		if (!port) return;
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const cur: Vec3 = [...(port.dir ?? [0, 0, 0])] as Vec3;
		const v = raw === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		cur[i] = v;
		// All blank: back to the default (out of the anchored face, else up).
		const next = { ...port };
		if (cur.every((c) => c === 0)) delete next.dir;
		else next.dir = cur;
		postOp({ type: 'setPort', name, port: next });
	}
	/** "Add port at top of Motor": a port anchored on the picked part's box (or the bounds). */
	function addPortAt(e: Event) {
		const sel = e.currentTarget as HTMLSelectElement;
		const v = sel.value;
		sel.value = '';
		if (!v) return;
		const [where, anchor] = v.split(':');
		const part = where === 'part' ? kd.picked : null;
		postOp({ type: 'setPort', name: portNameFrom(part ?? anchor, Object.keys(kind?.ports ?? {})), port: { at: part ? { part, anchor } : { anchor } } });
	}
	const ANCHORS = ['top', 'bottom', 'left', 'right', 'front', 'back', 'center'];
	function deletePort(name: string) {
		postOp({ type: 'deletePort', name });
	}
	function hoverPort(name: string | null) {
		if (kd.hoverPort !== name) kd.hoverPort = name;
	}
	const fmtHit = (v: Vec3) => v.map((c) => fmt(c)).join(', ');
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
		<div class="fact top">
			<span>label at</span>
			{#if kind.labelAt === undefined}
				<span class="muted">the top of the bounds · <button class="link" onclick={() => setLabelAt('top')}>set</button></span>
			{:else}
				<PointFields value={kind.labelAt} parts={kd.meshes} picked={kd.picked} name="label" words onchange={setLabelAt} />
			{/if}
		</div>
		{#if kind.labelAt !== undefined}<button class="link" onclick={clearLabelAt}>label at the top of the bounds (default)</button>{/if}
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

		<div class="head sub">Ports <span class="count">{ports.length}</span></div>
		{#each ports as [name, port] (name)}
			<div class="port" data-kind="port-row" data-name={name} role="group" aria-label="port {name}" onpointerenter={() => hoverPort(name)} onpointerleave={() => kd.hoverPort === name && hoverPort(null)}>
				<div class="row">
					<input class="nx-input pname" value={name} onchange={(e) => renamePort(e, name)} aria-label="port name" spellcheck="false" title="The port's name — what a pipe's from/to names" />
					<button class="link" onclick={() => movePortHere(name)} disabled={!kd.hit} title={kd.hit ? `Move this port to where you clicked (${fmtHit(kd.hit.at)})` : 'Click a part on the model first'}>here</button>
					<button class="del" onclick={() => deletePort(name)} title="Remove this port" aria-label="remove port">×</button>
				</div>
				<div class="fact top">
					<span>at</span>
					<PointFields value={port.at} parts={kd.meshes} picked={kd.picked} name="port at" onchange={(p) => portAt(name, p)} />
				</div>
				<div class="vec">
					<span>dir</span>
					{#each AXES as a, i (a)}<input class="nx-input" type="number" step="0.1" value={fmt(port.dir?.[i])} onchange={(e) => portDir(e, name, i)} aria-label="port dir {a}" title="{a} — the direction a pipe leaves; blank = out of the anchored face, else up" placeholder={Array.isArray(port.at) ? (i === 1 ? '1' : '0') : 'face'} />{/each}
				</div>
			</div>
		{/each}
		<div class="actions two">
			<button onclick={addPortHere} disabled={!kd.hit} title={kd.hit ? `A port where you clicked (${fmtHit(kd.hit.at)}), facing out of the surface` : 'Click a part on the model first — the port goes where you clicked'}>
				Add port here{#if kd.hit && kd.picked}<small>&nbsp;on {kd.picked}</small>{/if}
			</button>
			<select class="nx-input" value="" onchange={addPortAt} aria-label="add port at" data-kind="add-port-at" disabled={kd.modelState !== 'ok'} title="A port on a face of the picked part's box, or of the bounds — it survives a re-export of the model">
				<option value="" disabled>Add port at…</option>
				{#if kd.picked}
					{#each ANCHORS as a (a)}<option value="part:{a}">{a} of {kd.picked}</option>{/each}
				{/if}
				{#each ANCHORS as a (a)}<option value="bounds:{a}">{a} of the bounds</option>{/each}
			</select>
		</div>

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
	.fact.top {
		align-items: start;
	}
	.fact .muted {
		color: var(--nx-muted);
	}
	.actions.two {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 4px;
	}
	.actions select {
		width: 100%;
		min-width: 0;
		padding: 4px 6px;
		font-size: 12px;
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
	.actions button:hover:not(:disabled) {
		background: var(--nx-hover);
	}
	.actions button:disabled {
		opacity: 0.55;
		cursor: default;
	}
	.actions button small {
		opacity: 0.75;
	}
	.port {
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		padding: 4px 6px;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.port:hover {
		border-color: var(--nx-muted);
	}
	.port .row {
		display: flex;
		gap: 4px;
		align-items: center;
	}
	.port .pname {
		flex: 1;
		min-width: 0;
		font-family: var(--nx-mono);
	}
	.port .vec {
		grid-template-columns: 34px 1fr 1fr 1fr;
	}
	.port .link:disabled {
		opacity: 0.45;
		cursor: default;
	}
	.port .del {
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
	.port .del:hover {
		color: var(--nx-err);
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
