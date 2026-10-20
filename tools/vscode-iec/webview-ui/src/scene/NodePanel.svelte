<script lang="ts">
	// The panel: the selected node's fields, committed per change (blur /
	// Enter) as updateNode ops; nothing selected = the document. Optional
	// fields clear with an empty value (the host's patch null deletes the
	// key). Numbers are typed in metres and degrees, the document's units.
	import type { Vec3 } from '@joyautomation/nautilus-hmi-3d';
	import { ed, postOp } from './sceneState.svelte';

	const doc = $derived(ed.doc);
	const node = $derived(ed.selected && doc ? (doc.nodes.find((n) => n.id === ed.selected) ?? null) : null);
	const tagNames = $derived(Object.keys(ed.tags ?? {}).sort());

	function patch(p: Record<string, unknown>) {
		if (!node) return;
		postOp({ type: 'updateNode', id: node.id, patch: p });
	}
	function text(e: Event, key: string) {
		const v = (e.currentTarget as HTMLInputElement).value.trim();
		patch({ [key]: v === '' ? null : v });
	}
	function rename(e: Event) {
		const v = (e.currentTarget as HTMLInputElement).value.trim();
		if (!node || v === '' || v === node.id) return;
		patch({ id: v });
		ed.selected = v;
	}
	function vec(e: Event, key: 'pos' | 'rot', i: number) {
		if (!node) return;
		const raw = (e.currentTarget as HTMLInputElement).value;
		const v = raw.trim() === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		const cur: Vec3 = [...(node[key] ?? [0, 0, 0])] as Vec3;
		cur[i] = v;
		patch({ [key]: key === 'rot' && cur.every((c) => c === 0) ? null : cur });
	}
	function scale(e: Event) {
		const raw = (e.currentTarget as HTMLInputElement).value;
		const v = raw.trim() === '' ? 1 : Number(raw);
		if (!isFinite(v) || v <= 0) return;
		patch({ scale: v === 1 ? null : v });
	}
	function kind(e: Event) {
		patch({ kind: (e.currentTarget as HTMLSelectElement).value });
	}
	function del() {
		if (!node) return;
		postOp({ type: 'deleteNode', id: node.id });
		ed.selected = null;
	}
	function saveView() {
		if (!ed.view) return;
		postOp({ type: 'setCamera', pos: ed.view.pos, target: ed.view.target });
	}
	/** The toolbar's look preview, when it differs from the document's. */
	const previewLook = $derived(ed.look && ed.look !== (doc?.look ?? 'lit') ? ed.look : null);
	function useLook() {
		if (previewLook) postOp({ type: 'setLook', look: previewLook });
	}
	const AXES = ['x', 'y', 'z'] as const;
	const fmt = (v: number | undefined) => (v === undefined ? '' : String(Math.round(v * 1000) / 1000));
</script>

<aside aria-label="Properties">
	{#if node}
		<div class="head">Node</div>
		<label>
			<span>id</span>
			<input class="nx-input" value={node.id} onchange={rename} spellcheck="false" />
		</label>
		<label>
			<span>kind</span>
			<select class="nx-input" value={node.kind} onchange={kind}>
				{#if !ed.kinds.some((k) => k.name === node.kind)}<option value={node.kind}>{node.kind}</option>{/if}
				{#each ed.kinds as k (k.name)}<option value={k.name}>{k.name}</option>{/each}
			</select>
		</label>
		<label>
			<span>tag</span>
			<input class="nx-input" value={node.tag ?? ''} onchange={(e) => text(e, 'tag')} list="scene-tags" placeholder="struct tag" spellcheck="false" />
		</label>
		<label>
			<span>label</span>
			<input class="nx-input" value={node.label ?? ''} onchange={(e) => text(e, 'label')} placeholder={node.id} />
		</label>
		<div class="vec">
			<span>pos <small>m</small></span>
			{#each AXES as a, i (a)}
				<input class="nx-input" type="number" step="0.01" value={fmt(node.pos[i])} onchange={(e) => vec(e, 'pos', i)} aria-label="pos {a}" title={a} />
			{/each}
		</div>
		<div class="vec">
			<span>rot <small>°</small></span>
			{#each AXES as a, i (a)}
				<input class="nx-input" type="number" step="5" value={fmt(node.rot?.[i])} onchange={(e) => vec(e, 'rot', i)} aria-label="rot {a}" title={a} placeholder="0" />
			{/each}
		</div>
		<label>
			<span>scale</span>
			<input class="nx-input" type="number" step="0.1" min="0.01" value={fmt(node.scale)} onchange={scale} placeholder="1" />
		</label>
		{#if node.bind && Object.keys(node.bind).length}
			<div class="head sub">bind</div>
			<dl class="bind">
				{#each Object.entries(node.bind) as [k, ref] (k)}
					<dt>{k}</dt>
					<dd>{ref}</dd>
				{/each}
			</dl>
		{/if}
		<div class="actions">
			<button class="danger" onclick={del} title="Delete this node (Del)">Delete node</button>
		</div>
	{:else if doc}
		<div class="head">Scene</div>
		<dl class="facts">
			{#if doc.name}<dt>name</dt><dd>{doc.name}</dd>{/if}
			<dt>nodes</dt>
			<dd>{doc.nodes.length}</dd>
			<dt>pipes</dt>
			<dd>{doc.pipes?.length ?? 0}</dd>
			<dt>kinds</dt>
			<dd>{Object.keys(doc.kinds ?? {}).length}</dd>
			<dt>grid</dt>
			<dd>{doc.grid?.cell ?? 0.1} m</dd>
			<dt>look</dt>
			<dd>{doc.look ?? 'lit'}{#if previewLook} <i>(stage: {previewLook})</i>{/if}</dd>
		</dl>
		<div class="head sub">Camera</div>
		{#if ed.view}
			<div class="cam">
				<span>at</span><code>{ed.view.pos.map((v) => v.toFixed(2)).join(', ')}</code>
				<span>looking at</span><code>{ed.view.target.map((v) => v.toFixed(2)).join(', ')}</code>
			</div>
		{/if}
		<div class="actions">
			<button onclick={saveView} disabled={!ed.view} title="Write the current orbit view to the document's camera">Save view as the scene's camera</button>
			{#if previewLook}
				<button onclick={useLook} title="Write the look on stage to the document (look: {previewLook})">Use {previewLook} as the scene's look</button>
			{/if}
		</div>
		<p class="hint">Click a node to edit it. Pick a kind on the left, then click the ground to place one.</p>
	{/if}
</aside>

<datalist id="scene-tags">
	{#each tagNames as t (t)}<option value={t}></option>{/each}
</datalist>

<style>
	aside {
		width: 230px;
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
	label,
	.vec {
		display: grid;
		grid-template-columns: 44px 1fr;
		align-items: center;
		gap: 4px;
	}
	.vec {
		grid-template-columns: 44px 1fr 1fr 1fr;
	}
	label span,
	.vec span {
		color: var(--nx-muted);
	}
	small {
		opacity: 0.7;
	}
	.nx-input {
		width: 100%;
		min-width: 0;
		box-sizing: border-box;
		font-size: 12px;
		padding: 3px 5px;
	}
	input[type='number'] {
		font-family: var(--nx-mono);
	}
	dl {
		margin: 0;
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 2px 8px;
	}
	dt {
		color: var(--nx-muted);
	}
	dd {
		margin: 0;
		font-family: var(--nx-mono);
		overflow-wrap: anywhere;
	}
	.cam {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 2px 8px;
	}
	.cam span {
		color: var(--nx-muted);
	}
	.cam code {
		font-family: var(--nx-mono);
	}
	.actions {
		margin-top: 8px;
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
		opacity: 0.5;
		cursor: default;
	}
	.actions .danger {
		border-color: var(--nx-err);
		color: var(--nx-err);
	}
	.hint {
		color: var(--nx-muted);
		margin: 8px 0 0;
		line-height: 1.4;
	}
</style>
