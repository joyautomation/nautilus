<script lang="ts">
	// Context props panel: the selected thing's fields, committed per change
	// (blur/Enter) as ops. Nothing selected = document settings. Optional
	// fields clear with an empty value (patch null deletes the key).
	import { makeGetPort, resolvePipeEndpoints, type GetPort, type MimicEquipment, type MimicPipeAnchor } from '@joyautomation/nautilus-hmi';
	import BindEditor from './BindEditor.svelte';
	import { resolvePorts } from './ports';
	import { BIND_HINTS, registryNames } from './registry';
	import { suggestRoute, type ObstacleRect } from './autoroute';
	import { minInteriorPoints } from './pipeDraft';
	import { ed, postManifestOp, postOp, type MimicOp, type PortsEditTarget } from './mimicState.svelte';

	const doc = $derived(ed.doc);
	const sel = $derived(ed.selection);
	const eq = $derived(
		sel?.kind === 'equipment' && doc ? ((doc.equipment ?? []).find((e) => e.id === sel.id) ?? null) : null
	);
	// `kind: 'end'` (a pipe's terminal handle selected directly — see
	// mimicState.svelte.ts's Selection doc comment) shows the SAME panel as
	// a plain 'pipe' selection; only the keyboard nudge (EditorCanvas) tells
	// the two apart.
	const pipe = $derived(
		doc && (sel?.kind === 'pipe' || sel?.kind === 'end')
			? ((doc.pipes ?? []).find((p) => p.id === (sel.kind === 'pipe' ? sel.id : sel.pipeId)) ?? null)
			: null
	);
	const label = $derived(sel?.kind === 'label' && doc ? ((doc.labels ?? [])[sel.index] ?? null) : null);
	/** Which end (if any) is selected via the distinct 'end' kind — used
	 * only to highlight that row in the "ends" section below. */
	const selectedEnd = $derived(sel?.kind === 'end' ? sel.end : null);

	// ── pipe end anchors (Feature 2) ─────────────────────────────────────────
	// This panel has no DOM measurement of equipment boxes (that's
	// EditorCanvas's domain — see its eqBox()); a detach here APPROXIMATES
	// the freed point's position using the same unmeasured-fallback size
	// EditorCanvas itself uses before a component's first paint (width ??
	// 100, height 80). Good enough to land in the right neighborhood — the
	// point is concrete and immediately visible/draggable afterward, per the
	// "editor is never stricter than the JSON" principle; drag-to-detach on
	// the canvas (exact, DOM-measured) is the precise alternative.
	function estimateBox(e: MimicEquipment): { x: number; y: number; w: number; h: number } {
		return { x: e.x, y: e.y, w: e.width ?? 100, h: 80 };
	}
	function anchorAbsolute(ref: MimicPipeAnchor | undefined): [number, number] | null {
		if (!ref || !doc) return null;
		const anchorEq = (doc.equipment ?? []).find((e) => e.id === ref.equip);
		if (!anchorEq) return null;
		const port = resolvePorts(anchorEq, ed.manifest).find((p) => p.name === ref.port);
		if (!port) return null;
		const b = estimateBox(anchorEq);
		return [Math.round(b.x + port.x * b.w), Math.round(b.y + port.y * b.h)];
	}
	function detachEnd(end: 'from' | 'to') {
		if (!pipe) return;
		const ref = end === 'from' ? pipe.from : pipe.to;
		const point = anchorAbsolute(ref) ?? [0, 0];
		const pts = pipe.points.map((q) => [...q] as [number, number]);
		if (end === 'from') pts.unshift(point);
		else pts.push(point);
		postOp({ type: 'updatePipe', id: pipe.id, patch: { [end]: null, points: pts } });
	}
	/** ROUTE SUGGESTION (item 3): replace a pipe's interior vertices with a
	 * fresh suggestion — an ordinary, undoable setPipePoints op, same as any
	 * hand drag. Uses the same estimateBox() approximation detachEnd/
	 * anchorAbsolute already rely on (this panel has no DOM measurement);
	 * available for any pipe with at least one anchored end (a fully
	 * floating pipe has no ports to route between). */
	function reroute() {
		if (!pipe || !doc || (!pipe.from && !pipe.to)) return;
		const getPortApprox: GetPort = makeGetPort(
			(id) => {
				const e = (doc.equipment ?? []).find((x) => x.id === id);
				return e ? estimateBox(e) : undefined;
			},
			(id) => {
				const e = (doc.equipment ?? []).find((x) => x.id === id);
				return e ? resolvePorts(e, ed.manifest) : undefined;
			}
		);
		const resolved = resolvePipeEndpoints({ points: pipe.points, from: pipe.from, to: pipe.to }, getPortApprox);
		const full = resolved.points;
		const start = { x: full[0][0], y: full[0][1] };
		const end = { x: full[full.length - 1][0], y: full[full.length - 1][1] };
		const exclude = new Set<string>();
		if (pipe.from) exclude.add(pipe.from.equip);
		if (pipe.to) exclude.add(pipe.to.equip);
		const margin = 16;
		const obstacles: ObstacleRect[] = (doc.equipment ?? [])
			.filter((e) => !exclude.has(e.id))
			.map((e) => {
				const b = estimateBox(e);
				return { x: b.x - margin, y: b.y - margin, w: b.w + 2 * margin, h: b.h + 2 * margin };
			});
		const points = suggestRoute(start, resolved.startDir, end, resolved.endDir, obstacles);
		postOp({ type: 'setPipePoints', id: pipe.id, points });
	}

	function attachEnd(end: 'from' | 'to', equip: string, port: string) {
		if (!pipe || !equip || !port) return;
		const wasAnchored = !!(end === 'from' ? pipe.from : pipe.to);
		const pts = pipe.points.map((q) => [...q] as [number, number]);
		if (!wasAnchored) {
			if (end === 'from') pts.shift();
			else pts.pop();
		}
		postOp({ type: 'updatePipe', id: pipe.id, patch: { [end]: { equip, port }, points: pts } });
	}
	// In-progress equipment pick for the attach dropdowns (the port select's
	// options depend on it), one per end.
	let fromAttachEquip = $state('');
	let toAttachEquip = $state('');
	$effect(() => {
		void pipe?.id;
		fromAttachEquip = '';
		toAttachEquip = '';
	});
	const portsOf = (equipId: string): { name: string }[] => {
		const e = (doc?.equipment ?? []).find((x) => x.id === equipId);
		return e ? resolvePorts(e, ed.manifest) : [];
	};

	const num = (s: string): number | null => {
		const n = Number(s);
		return s.trim() === '' || !isFinite(n) ? null : n;
	};

	function patchEq(patch: Record<string, unknown>) {
		if (eq) postOp({ type: 'updateEquipment', id: eq.id, patch });
	}
	function patchPipe(patch: Record<string, unknown>) {
		if (pipe) postOp({ type: 'updatePipe', id: pipe.id, patch });
	}
	function renameEq(id: string) {
		if (!eq || id === '' || id === eq.id) return;
		patchEq({ id });
		ed.selection = { kind: 'equipment', id };
	}
	function renamePipe(id: string) {
		if (!pipe || id === '' || id === pipe.id) return;
		patchPipe({ id });
		ed.selection = { kind: 'pipe', id };
	}

	// ── ports editor ─────────────────────────────────────────────────────────
	const portsEditing = $derived(!!eq && ed.portsEdit?.id === eq.id);
	const portsTarget = $derived(ed.portsEdit?.target ?? 'manifest');
	function togglePortsEdit() {
		if (!eq) return;
		ed.portsEdit = portsEditing ? null : { id: eq.id, target: 'manifest' };
	}
	function setPortsTarget(target: PortsEditTarget) {
		if (!eq) return;
		ed.portsEdit = { id: eq.id, target };
	}
	/** Drop the instance override (null) so this instance goes back to
	 * inheriting from the manifest/built-in default. */
	function clearInstancePorts() {
		if (!eq) return;
		postOp({ type: 'setEquipmentPorts', id: eq.id, ports: null });
	}
	/** Drop the manifest's ports entry for this component — every instance
	 * that doesn't have its own override reverts to the built-in default. */
	function clearManifestPorts() {
		if (!eq) return;
		postManifestOp({ type: 'setComponentPorts', component: eq.component, ports: null });
	}
	/** null = no manifest entry for this component (nothing to show/clear). */
	const manifestPortCount = $derived.by((): number | null => {
		if (!eq) return null;
		const p = ed.manifest?.[eq.component]?.ports;
		return p ? p.length : null;
	});

	// Static props JSON: parse on commit; invalid stays local + marked.
	let propsErr = $state(false);
	$effect(() => {
		void sel;
		propsErr = false;
	});
	function commitProps(raw: string, target: 'eq' | 'pipe') {
		let v: unknown;
		try {
			v = raw.trim() === '' ? {} : JSON.parse(raw);
		} catch {
			propsErr = true;
			return;
		}
		if (typeof v !== 'object' || v === null || Array.isArray(v)) {
			propsErr = true;
			return;
		}
		propsErr = false;
		const patch = { props: Object.keys(v).length ? v : null };
		if (target === 'eq') patchEq(patch);
		else patchPipe(patch);
	}

	/** Deleting equipment materializes any pipe anchored to it in the SAME
	 * batch — same rule as EditorCanvas's keyboard Delete (see
	 * deleteEquipmentWithAnchors there for the DOM-measured, exact version
	 * of this same idea); this one uses estimateBox()'s approximation since
	 * this panel has no canvas geometry to measure from. */
	function deleteEquipmentWithAnchors(eqId: string) {
		if (!doc) {
			postOp({ type: 'deleteEquipment', id: eqId });
			return;
		}
		const ops: import('./mimicState.svelte').MimicOp[] = [];
		for (const p of doc.pipes ?? []) {
			const fromHit = p.from?.equip === eqId;
			const toHit = p.to?.equip === eqId;
			if (!fromHit && !toHit) continue;
			const pts = p.points.map((q) => [...q] as [number, number]);
			const patch: Record<string, unknown> = {};
			if (fromHit) {
				pts.unshift(anchorAbsolute(p.from) ?? [0, 0]);
				patch.from = null;
			}
			if (toHit) {
				pts.push(anchorAbsolute(p.to) ?? [0, 0]);
				patch.to = null;
			}
			patch.points = pts;
			ops.push({ type: 'updatePipe', id: p.id, patch });
		}
		ops.push({ type: 'deleteEquipment', id: eqId });
		postOp(ops.length > 1 ? { type: 'batch', ops } : ops[0]);
	}

	function deleteSel() {
		if (!sel) return;
		const s = sel;
		ed.selection = null;
		if (s.kind === 'equipment') deleteEquipmentWithAnchors(s.id);
		else if (s.kind === 'pipe') postOp({ type: 'deletePipe', id: s.id });
		else if (s.kind === 'end') postOp({ type: 'deletePipe', id: s.pipeId });
		else if (s.kind === 'label') postOp({ type: 'deleteLabel', index: s.index });
		// 'nodes' is handled by deleteSelectedNodes() below, wired to its own
		// panel button — its op needs the pipe's CURRENT points, which this
		// generic path (called with the selection already nulled) doesn't have.
	}

	/** Delete every point in a 'nodes' (node multi-select) selection — the
	 * SAME reducer EditorCanvas's Delete/Backspace key uses (mirrored here,
	 * not shared, since this panel has no canvas/DOM geometry to depend on;
	 * minInteriorPoints — pipeDraft.ts's pure logic — is the one piece
	 * actually shared rather than re-derived). Exists so a mouse-only user
	 * (or anyone who didn't realize Delete/Backspace was the mechanism) has
	 * an explicit, discoverable affordance for removing a node selection —
	 * see BUG 1: shift-click multi-select had no visible "now what?" beyond
	 * a note pointing at a keyboard shortcut. */
	function deleteSelectedNodes() {
		if (!doc || sel?.kind !== 'nodes') return;
		const s = sel;
		ed.selection = null;
		const p = (doc.pipes ?? []).find((pp) => pp.id === s.pipeId);
		if (!p) return;
		const pts = p.points.filter((_, i) => !s.indices.includes(i));
		if (pts.length < minInteriorPoints(!!p.from, !!p.to)) postOp({ type: 'deletePipe', id: s.pipeId });
		else postOp({ type: 'setPipePoints', id: s.pipeId, points: pts });
	}
</script>

<aside aria-label="Properties">
	{#if eq && sel?.kind === 'equipment'}
		<h4>{eq.component} <span class="dim">· {eq.id}</span></h4>
		<div class="field">
			<span>id</span>
			<input class="nx-input" value={eq.id} onchange={(e) => renameEq(e.currentTarget.value)} />
		</div>
		<div class="field">
			<span>component</span>
			<select
				class="nx-input"
				value={eq.component}
				onchange={(e) => patchEq({ component: e.currentTarget.value })}
			>
				{#each registryNames.includes(eq.component) ? registryNames : [eq.component, ...registryNames] as n (n)}
					<option value={n}>{n}</option>
				{/each}
			</select>
		</div>
		<div class="pair">
			<div class="field">
				<span>x</span>
				<input
					class="nx-input"
					type="number"
					value={eq.x}
					onchange={(e) => postOp({ type: 'moveEquipment', id: eq.id, x: num(e.currentTarget.value) ?? eq.x, y: eq.y })}
				/>
			</div>
			<div class="field">
				<span>y</span>
				<input
					class="nx-input"
					type="number"
					value={eq.y}
					onchange={(e) => postOp({ type: 'moveEquipment', id: eq.id, x: eq.x, y: num(e.currentTarget.value) ?? eq.y })}
				/>
			</div>
		</div>
		<div class="pair">
			<div class="field">
				<span>width</span>
				<input
					class="nx-input"
					type="number"
					placeholder="auto"
					value={eq.width ?? ''}
					onchange={(e) => patchEq({ width: num(e.currentTarget.value) })}
				/>
			</div>
			<div class="field">
				<span>label</span>
				<input
					class="nx-input"
					value={eq.label ?? ''}
					onchange={(e) => patchEq({ label: e.currentTarget.value === '' ? null : e.currentTarget.value })}
				/>
			</div>
		</div>
		<div class="field">
			<span>ports</span>
			<button class="nx-input portbtn" class:active={portsEditing} onclick={togglePortsEdit}>
				{portsEditing ? 'Done editing ports' : 'Edit ports…'}
			</button>
			{#if portsEditing}
				<div class="portstarget" role="radiogroup" aria-label="Ports edit target">
					<button
						class:active={portsTarget === 'manifest'}
						title="Every {eq.component} in the project inherits this"
						onclick={() => setPortsTarget('manifest')}
					>
						Shared ({eq.component})
					</button>
					<button
						class:active={portsTarget === 'instance'}
						title="Only this instance ({eq.id})"
						onclick={() => setPortsTarget('instance')}
					>
						This instance only
					</button>
				</div>
				<p class="note">Drag a dot to move it, double-click to remove. Double-click the box to add one.</p>
			{/if}
			{#if eq.ports !== undefined}
				<p class="note">Instance override active ({eq.ports.length} port{eq.ports.length === 1 ? '' : 's'}).</p>
				<button class="portbtn" onclick={clearInstancePorts}>Revert to shared ports</button>
			{/if}
			{#if manifestPortCount !== null}
				<p class="note">
					Shared ports set for {eq.component} ({manifestPortCount} port{manifestPortCount === 1 ? '' : 's'}).
				</p>
				<button class="portbtn" onclick={clearManifestPorts}>Clear shared ports</button>
			{/if}
		</div>
		{#key eq.id}
			<BindEditor
				binds={eq.bind}
				hints={BIND_HINTS[eq.component] ?? []}
				onchange={(next) => patchEq({ bind: next })}
			/>
			<div class="field">
				<span>props (JSON)</span>
				<textarea
					class="nx-input"
					class:bad={propsErr}
					rows="3"
					spellcheck="false"
					value={eq.props ? JSON.stringify(eq.props) : ''}
					onchange={(e) => commitProps(e.currentTarget.value, 'eq')}
				></textarea>
			</div>
		{/key}
		<button class="del" onclick={deleteSel}>Delete {eq.id}</button>
	{:else if pipe && (sel?.kind === 'pipe' || sel?.kind === 'end')}
		<h4>Pipe <span class="dim">· {pipe.id}</span></h4>
		<div class="field">
			<span>id</span>
			<input class="nx-input" value={pipe.id} onchange={(e) => renamePipe(e.currentTarget.value)} />
		</div>
		<div class="field">
			<span>color</span>
			<input
				class="nx-input"
				placeholder="var(--s1)"
				value={pipe.color ?? ''}
				list="mimic-colors"
				onchange={(e) => patchPipe({ color: e.currentTarget.value === '' ? null : e.currentTarget.value })}
			/>
			<datalist id="mimic-colors">
				<option value="var(--s1)"></option>
				<option value="var(--s2)"></option>
				<option value="var(--s3)"></option>
				<option value="var(--good)"></option>
				<option value="var(--warn)"></option>
				<option value="var(--crit)"></option>
			</datalist>
		</div>
		<div class="field">
			<span>routing</span>
			<select
				class="nx-input"
				value={pipe.routing ?? 'direct'}
				onchange={(e) =>
					patchPipe({ routing: e.currentTarget.value === 'direct' ? null : e.currentTarget.value })}
			>
				<option value="direct">Direct (straight segments)</option>
				<option value="orthogonal">Orthogonal (90° corners)</option>
			</select>
		</div>
		<p class="note">{pipe.points.length} points — drag circles to move, squares to add, double-click to remove</p>
		{#if pipe.from || pipe.to}
			<button class="portbtn" onclick={reroute}>Re-route</button>
			<p class="note">Replaces the interior points with a fresh suggested route (undoable).</p>
		{/if}
		<div class="field anchors">
			<span>ends</span>
			{#each (['from', 'to'] as const) as end (end)}
				{@const ref = end === 'from' ? pipe.from : pipe.to}
				{@const resolved = ref ? anchorAbsolute(ref) : null}
				<div class="anchorrow" class:sel={selectedEnd === end}>
					<span class="endlabel">{end}</span>
					{#if ref}
						<span class="anchortag" class:bad={!resolved}
							>{ref.equip}.{ref.port}{!resolved ? ' (unresolved)' : ''}</span
						>
						<button class="detachbtn" title="Detach — becomes a concrete point" onclick={() => detachEnd(end)}>✕</button>
					{:else}
						<select
							class="nx-input"
							value={end === 'from' ? fromAttachEquip : toAttachEquip}
							onchange={(e) => {
								if (end === 'from') fromAttachEquip = e.currentTarget.value;
								else toAttachEquip = e.currentTarget.value;
							}}
						>
							<option value="">equipment…</option>
							{#each doc?.equipment ?? [] as e (e.id)}
								<option value={e.id}>{e.id}</option>
							{/each}
						</select>
						{@const picked = end === 'from' ? fromAttachEquip : toAttachEquip}
						{#if picked}
							<select class="nx-input" value="" onchange={(e) => attachEnd(end, picked, e.currentTarget.value)}>
								<option value="" disabled>port…</option>
								{#each portsOf(picked) as p (p.name)}
									<option value={p.name}>{p.name}</option>
								{/each}
							</select>
						{/if}
					{/if}
				</div>
			{/each}
		</div>
		{#key pipe.id}
			<BindEditor
				binds={pipe.bind}
				hints={BIND_HINTS.pipe}
				onchange={(next) => patchPipe({ bind: next })}
			/>
			<div class="field">
				<span>props (JSON)</span>
				<textarea
					class="nx-input"
					class:bad={propsErr}
					rows="3"
					spellcheck="false"
					value={pipe.props ? JSON.stringify(pipe.props) : ''}
					onchange={(e) => commitProps(e.currentTarget.value, 'pipe')}
				></textarea>
			</div>
		{/key}
		<button class="del" onclick={deleteSel}>Delete {pipe.id}</button>
	{:else if label && sel?.kind === 'label'}
		<h4>Label</h4>
		<div class="field">
			<span>text</span>
			<input
				class="nx-input"
				value={label.text}
				onchange={(e) => postOp({ type: 'updateLabel', index: sel.index, patch: { text: e.currentTarget.value } })}
			/>
		</div>
		<div class="pair">
			<div class="field">
				<span>x</span>
				<input
					class="nx-input"
					type="number"
					value={label.x}
					onchange={(e) =>
						postOp({ type: 'updateLabel', index: sel.index, patch: { x: num(e.currentTarget.value) ?? label.x } })}
				/>
			</div>
			<div class="field">
				<span>y</span>
				<input
					class="nx-input"
					type="number"
					value={label.y}
					onchange={(e) =>
						postOp({ type: 'updateLabel', index: sel.index, patch: { y: num(e.currentTarget.value) ?? label.y } })}
				/>
			</div>
		</div>
		<button class="del" onclick={deleteSel}>Delete label</button>
	{:else if sel?.kind === 'multi'}
		<h4>Equipment <span class="dim">· {sel.ids.length} selected</span></h4>
		<p class="note">{sel.ids.join(', ')}</p>
		<p class="note">Ctrl+C / Ctrl+X copy or cut them (with the pipes between them), Ctrl+V pastes, Ctrl+D duplicates, arrows nudge, Del deletes. Ctrl-click adds or removes one.</p>
	{:else if sel?.kind === 'nodes'}
		<h4>Nodes <span class="dim">· {sel.pipeId}</span></h4>
		<p class="note">{sel.indices.length} point{sel.indices.length === 1 ? '' : 's'} selected on this pipe.</p>
		<p class="note">Shift-click a vertex to add or remove it.</p>
		<button class="del" onclick={deleteSelectedNodes}
			>Delete {sel.indices.length} point{sel.indices.length === 1 ? '' : 's'}</button
		>
	{:else if doc}
		<h4>Document</h4>
		<div class="field">
			<span>name</span>
			<input
				class="nx-input"
				value={doc.name ?? ''}
				placeholder="display name"
				onchange={(e) => postOp({ type: 'setName', name: e.currentTarget.value === '' ? null : e.currentTarget.value })}
			/>
		</div>
		<div class="pair">
			<div class="field">
				<span>canvas w</span>
				<input
					class="nx-input"
					type="number"
					value={doc.canvas.width}
					onchange={(e) =>
						postOp({ type: 'setCanvas', width: num(e.currentTarget.value) ?? doc.canvas.width, height: doc.canvas.height })}
				/>
			</div>
			<div class="field">
				<span>canvas h</span>
				<input
					class="nx-input"
					type="number"
					value={doc.canvas.height}
					onchange={(e) =>
						postOp({ type: 'setCanvas', width: doc.canvas.width, height: num(e.currentTarget.value) ?? doc.canvas.height })}
				/>
			</div>
		</div>
		<p class="note">
			{(doc.equipment ?? []).length} equipment · {(doc.pipes ?? []).length} pipes · {(doc.labels ?? []).length} labels
		</p>
		<p class="note">Select something on the canvas to edit it; the JSON stays canonical and diffable.</p>
	{/if}
</aside>

<style>
	aside {
		flex: none;
		width: 230px;
		overflow-y: auto;
		border-left: 1px solid var(--nx-border);
		padding: 10px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	h4 {
		margin: 0;
		font-size: 12px;
		color: var(--nx-ink);
	}
	.dim {
		color: var(--nx-muted);
		font-weight: 400;
		font-family: var(--nx-mono);
	}
	.field {
		display: grid;
		grid-template-columns: minmax(0, 1fr); /* load-bearing: number inputs' intrinsic width must not set the column */
		gap: 3px;
		min-width: 0;
	}
	.field > span {
		font-size: 11px;
		color: var(--nx-muted);
	}
	.field :global(.nx-input) {
		width: 100%;
		min-width: 0;
	}
	.pair {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 6px;
		min-width: 0;
	}
	textarea.nx-input {
		resize: vertical;
		font-size: 11px;
	}
	.nx-input.bad {
		border-color: var(--nx-err);
	}
	.note {
		margin: 0;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.del {
		margin-top: 4px;
		padding: 4px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		background: transparent;
		color: var(--nx-err);
		cursor: pointer;
		font-size: 12px;
	}
	.del:hover {
		background: var(--nx-err-bg);
	}
	.portbtn {
		display: block;
		width: 100%;
		text-align: left;
		padding: 4px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
		margin-top: 2px;
	}
	.portbtn:hover {
		background: var(--nx-hover);
	}
	.portbtn.active {
		border-color: var(--nx-sel-ink);
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.portstarget {
		display: flex;
		gap: 4px;
		margin-top: 4px;
	}
	.portstarget button {
		flex: 1;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		background: transparent;
		color: var(--nx-ui-ink);
		padding: 3px 6px;
		font-size: 11px;
		cursor: pointer;
	}
	.portstarget button:hover {
		background: var(--nx-hover);
	}
	.portstarget button.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
		border-color: var(--nx-sel-ink);
	}
	.anchors {
		gap: 6px;
	}
	.anchorrow {
		display: flex;
		align-items: center;
		gap: 4px;
		flex-wrap: wrap;
		border-radius: 4px;
	}
	/* This end is the specific one addressed by a 'kind: end' selection
	   (clicked directly, or arrow-key nudge is currently targeting it). */
	.anchorrow.sel {
		outline: 1px solid var(--nx-sel-ink);
		background: var(--nx-sel-bg);
	}
	.anchorrow :global(select.nx-input) {
		width: auto;
		flex: 1;
		min-width: 0;
	}
	.endlabel {
		font-family: var(--nx-mono);
		font-size: 11px;
		color: var(--nx-muted);
		width: 32px;
		flex: none;
	}
	.anchortag {
		font-family: var(--nx-mono);
		font-size: 11px;
		color: var(--nx-ink);
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.anchortag.bad {
		color: var(--nx-err);
	}
	.detachbtn {
		border: none;
		background: transparent;
		color: var(--nx-err);
		cursor: pointer;
		font-size: 12px;
		line-height: 1;
		padding: 2px 4px;
		border-radius: 3px;
		flex: none;
	}
	.detachbtn:hover {
		background: var(--nx-err-bg);
	}
</style>
