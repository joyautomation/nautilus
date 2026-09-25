<script lang="ts">
	// The mimic editor shell: toolbar (tools + live pill), equipment palette,
	// the canvas, and the context props panel. Document state arrives from
	// the extension host (mimicDoc / mimicError / mimicTags messages); every
	// gesture goes back as an op.
	import ShortcutHelp from '../ShortcutHelp.svelte';
	import { MIMIC_SHORTCUTS, hintLine } from '../shortcuts';
	import { announceReady, ed, reopenAsText, setSnapToGrid, toggleLive } from './mimicState.svelte';
	import EditorCanvas from './EditorCanvas.svelte';
	import EquipPalette from './EquipPalette.svelte';
	import PropsPanel from './PropsPanel.svelte';
	import { setUserComponentDiagnostics } from './userComponentsState.svelte';

	$effect(() => {
		const onMsg = (e: MessageEvent) => {
			const m = e.data as {
				type?: string;
				doc?: typeof ed.doc;
				title?: string;
				message?: string;
				tags?: Record<string, unknown> | null;
				components?: typeof ed.manifest;
				customComponents?: string[];
				diagnostics?: { component: string; message: string }[];
				snapToGrid?: boolean;
				enabled?: boolean;
			};
			if (m?.type === 'mimicDoc' && m.doc) {
				ed.doc = m.doc;
				ed.title = m.title ?? ed.title;
				ed.error = '';
			} else if (m?.type === 'mimicError') {
				ed.error = m.message ?? 'invalid document';
			} else if (m?.type === 'mimicTags') {
				ed.tags = m.tags ?? null;
				ed.liveEnabled = m.enabled ?? true;
			} else if (m?.type === 'mimicManifest') {
				ed.manifest = m.components ?? {};
				ed.customComponents = m.customComponents ?? [];
			} else if (m?.type === 'userComponentDiagnostics') {
				setUserComponentDiagnostics(m.diagnostics ?? []);
			} else if (m?.type === 'mimicConfig') {
				ed.snapToGrid = m.snapToGrid ?? true;
			}
		};
		window.addEventListener('message', onMsg);
		// Harness hook: a preloaded model outside VS Code.
		if (window.__MODEL__) onMsg(new MessageEvent('message', { data: window.__MODEL__ }));
		announceReady();
		return () => window.removeEventListener('message', onMsg);
	});

	// Prune a selection whose target vanished (deleted via text editing).
	$effect(() => {
		const s = ed.selection;
		const d = ed.doc;
		if (!s || !d) return;
		const gone =
			(s.kind === 'equipment' && !(d.equipment ?? []).some((e) => e.id === s.id)) ||
			(s.kind === 'pipe' && !(d.pipes ?? []).some((p) => p.id === s.id)) ||
			(s.kind === 'end' && !(d.pipes ?? []).some((p) => p.id === s.pipeId)) ||
			(s.kind === 'nodes' &&
				(() => {
					const p = (d.pipes ?? []).find((p) => p.id === s.pipeId);
					return !p || s.indices.some((i) => i >= p.points.length);
				})()) ||
			(s.kind === 'label' && !(d.labels ?? [])[s.index]);
		if (gone) ed.selection = null;
		else if (s.kind === 'multi') {
			const left = s.ids.filter((id) => (d.equipment ?? []).some((e) => e.id === id));
			if (left.length !== s.ids.length) ed.selection = left.length > 1 ? { kind: 'multi', ids: left } : left.length ? { kind: 'equipment', id: left[0] } : null;
		}
	});

	// Ports editing is scoped to one selected equipment instance — drop it
	// the moment that's no longer true (selection moved on, or the
	// instance itself was deleted/renamed out from under it).
	$effect(() => {
		const pe = ed.portsEdit;
		if (!pe) return;
		const s = ed.selection;
		const stillSelected = s?.kind === 'equipment' && s.id === pe.id;
		const exists = (ed.doc?.equipment ?? []).some((e) => e.id === pe.id);
		if (!stillSelected || !exists) ed.portsEdit = null;
	});

	const portsEditEq = $derived(
		ed.portsEdit ? ((ed.doc?.equipment ?? []).find((e) => e.id === ed.portsEdit!.id) ?? null) : null
	);

	const hint = $derived(
		ed.portsEdit && portsEditEq
			? `ports: drag a dot to move · double-click a dot to remove · double-click the box to add · select a dot (drag it or the panel row) then arrows nudge it (Shift = grid) · set each port's exit direction in the panel (→) · editing ${
					ed.portsEdit.target === 'manifest' ? `shared ${portsEditEq.component} ports (all instances)` : 'this instance only'
				} · Esc / "Done" exits`
			: ed.tool === 'pipe'
				? 'click to add points · starting/ending on a port dot anchors that end · Enter / double-click finishes · Shift = free angle · Esc cancels'
				: ed.tool === 'place'
					? `click to place ${ed.placeComponent} · Esc cancels`
					: ed.tool === 'label'
						? 'click to place a label · Esc cancels'
						: hintLine(MIMIC_SHORTCUTS)
	);

	function setTool(t: 'select' | 'pipe' | 'label') {
		ed.tool = t;
		ed.placeComponent = '';
		ed.portsEdit = null;
	}
</script>

<div class="editor">
	<header>
		<span class="title">{ed.title}</span>
		{#if ed.doc?.name}<span class="name">· {ed.doc.name}</span>{/if}
		<span class="tools" role="toolbar" aria-label="Tools">
			<button class:active={ed.tool === 'select'} onclick={() => setTool('select')}>Select</button>
			<button class:active={ed.tool === 'pipe'} onclick={() => setTool('pipe')}>+ Pipe</button>
			<button class:active={ed.tool === 'label'} onclick={() => setTool('label')}>+ Label</button>
		</span>
		<button
			class="snap-toggle"
			class:active={ed.snapToGrid}
			onclick={() => setSnapToGrid(!ed.snapToGrid)}
			title="Snap dragged equipment, labels and pipe vertices to the grid. Shift-arrow nudging always snaps regardless of this toggle."
		>
			Snap: {ed.snapToGrid ? 'On' : 'Off'}
		</button>
		<span class="spacer"></span>
		<ShortcutHelp groups={MIMIC_SHORTCUTS} />
		<!-- same toggle as the diagrams' live pill and the status-bar item -->
		<button
			class="live nx-pill"
			class:off={!ed.liveEnabled || !ed.tags}
			onclick={toggleLive}
			title={!ed.liveEnabled
				? 'Live values are off — click to enable'
				: ed.tags
					? 'Live tags from nautilus.runtimeUrl — bound equipment animates with the process. Click to disable'
					: 'Live values enabled but the controller isn\'t answering — is it running? Click to disable'}
		>
			{!ed.liveEnabled ? '○ live off' : ed.tags ? '● live' : '◌ offline'}
		</button>
	</header>

	{#if ed.error && !ed.doc}
		<!-- Never parsed: there's no canvas to show, only the way out. -->
		<div class="fatal" role="alert">
			<p>This file isn't a mimic document the editor can open:</p>
			<pre>{ed.error}</pre>
			<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
		</div>
	{:else}
		{#if ed.error}
			<div class="err" role="alert">
				<span>JSON: {ed.error} — the canvas is read-only until the text parses again</span>
				<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
			</div>
		{/if}

		<!-- A parse error mid-edit leaves the last good doc on screen, locked:
		     gestures against it would only be refused. -->
		<div class="cols" class:locked={!!ed.error} inert={!!ed.error}>
			<EquipPalette />
			<EditorCanvas />
			<PropsPanel />
		</div>
	{/if}

	<footer title={hint}>{hint}</footer>

	<datalist id="mimic-tags">
		{#each Object.keys(ed.tags ?? {}) as t (t)}<option value={t}></option>{/each}
	</datalist>
</div>

<style>
	.editor {
		height: 100%;
		display: flex;
		flex-direction: column;
	}
	header {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-bottom: 1px solid var(--nx-border);
		flex: none;
	}
	.title {
		font-weight: 600;
		color: var(--nx-ink);
	}
	.name {
		color: var(--nx-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.tools {
		display: inline-flex;
		gap: 2px;
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		padding: 2px;
	}
	.tools button {
		border: none;
		background: transparent;
		color: var(--nx-ui-ink);
		font: inherit;
		font-size: 12px;
		padding: 3px 10px;
		border-radius: 4px;
		cursor: pointer;
	}
	.tools button:hover {
		background: var(--nx-hover);
	}
	.tools button.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.snap-toggle {
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		background: transparent;
		color: var(--nx-ui-ink);
		font: inherit;
		font-size: 12px;
		padding: 4px 10px;
		cursor: pointer;
	}
	.snap-toggle:hover {
		background: var(--nx-hover);
	}
	.snap-toggle.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
		border-color: var(--nx-sel-ink);
	}
	.spacer {
		flex: 1;
	}
	.live {
		font: inherit;
		font-size: 11px;
		font-weight: 600;
		padding: 2px 8px;
		cursor: pointer;
	}
	.live.off {
		color: var(--nx-muted);
		background: transparent;
		border-color: var(--nx-border);
	}
	.err {
		flex: none;
		padding: 4px 10px;
		font-family: var(--nx-mono);
		font-size: 12px;
		color: var(--nx-err);
		background: var(--nx-err-bg);
		border-bottom: 1px solid var(--nx-border);
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.err span {
		flex: 1;
		min-width: 0;
	}
	.reopen {
		flex: none;
		font: inherit;
		font-size: 12px;
		padding: 3px 10px;
		border: none;
		border-radius: 4px;
		background: var(--nx-btn-bg);
		color: var(--nx-btn-ink);
		cursor: pointer;
	}
	.fatal {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		padding: 24px;
		color: var(--nx-ui-ink);
	}
	.fatal p {
		margin: 0;
	}
	.fatal pre {
		margin: 0;
		max-width: 100%;
		white-space: pre-wrap;
		font-family: var(--nx-mono);
		font-size: 12px;
		color: var(--nx-err);
		background: var(--nx-err-bg);
		padding: 6px 10px;
		border-radius: 4px;
	}
	.cols {
		flex: 1;
		display: flex;
		min-height: 0;
	}
	.cols.locked {
		opacity: 0.5;
		filter: grayscale(0.6);
		pointer-events: none;
	}
	footer {
		flex: none;
		padding: 4px 10px;
		border-top: 1px solid var(--nx-border);
		color: var(--nx-muted);
		font-size: 11px;
	}
</style>
