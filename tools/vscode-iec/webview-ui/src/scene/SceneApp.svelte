<script lang="ts">
	// The scene editor shell (sceneEditor.ts opens it): toolbar (gizmo mode,
	// snap, live pill), the kinds palette, the stage, and the node panel.
	// Document state arrives from the extension host (sceneDoc / sceneError
	// / sceneTags / sceneConfig messages); every gesture goes back as an op.
	import { LOOKS, type SceneLook } from '@joyautomation/nautilus-hmi-3d';
	import { announceReady, deleteSelectedVertex, disarm, ed, missingAssets, postOp, reopenAsText, selectPipe, setSnapToGrid, stageLook, toggleLive } from './sceneState.svelte';
	import KindPalette from './KindPalette.svelte';
	import Stage3D from './Stage3D.svelte';
	import NodePanel from './NodePanel.svelte';

	$effect(() => {
		const onMsg = (e: MessageEvent) => {
			const m = e.data as {
				type?: string;
				doc?: typeof ed.doc;
				rendered?: typeof ed.rendered;
				kinds?: typeof ed.kinds;
				assets?: typeof ed.assets;
				kindFiles?: typeof ed.kindFiles;
				title?: string;
				message?: string;
				tags?: Record<string, unknown> | null;
				enabled?: boolean;
				snapToGrid?: boolean;
			};
			if (m?.type === 'sceneDoc' && m.doc) {
				ed.doc = m.doc;
				ed.rendered = m.rendered ?? m.doc;
				ed.kinds = m.kinds ?? [];
				ed.assets = m.assets ?? [];
				ed.kindFiles = m.kindFiles ?? [];
				ed.title = m.title ?? ed.title;
				ed.error = '';
			} else if (m?.type === 'sceneError') {
				ed.error = m.message ?? 'invalid document';
			} else if (m?.type === 'sceneTags') {
				ed.tags = m.tags ?? null;
				ed.liveEnabled = m.enabled ?? true;
			} else if (m?.type === 'sceneConfig') {
				ed.snapToGrid = m.snapToGrid ?? true;
			}
		};
		window.addEventListener('message', onMsg);
		// Harness hook: a preloaded model outside VS Code.
		if (window.__MODEL__) onMsg(new MessageEvent('message', { data: window.__MODEL__ }));
		announceReady();
		return () => window.removeEventListener('message', onMsg);
	});

	// A look preview that the document now has is no longer a preview.
	$effect(() => {
		if (ed.look && ed.look === (ed.doc?.look ?? 'lit')) ed.look = null;
	});
	const LOOK_TIP: Record<SceneLook, string> = {
		lit: 'Lit: materials and lights, the glTF models (the document\'s default)',
		flat: 'Flat: the models and primitives under plain lights, no environment',
		paper: 'Paper: the scene as a line drawing — white fills, inked edges'
	};

	// Prune a selection whose node vanished (deleted via text editing).
	$effect(() => {
		const s = ed.selected;
		const d = ed.doc;
		if (s && d && !d.nodes.some((n) => n.id === s)) ed.selected = null;
	});
	// And a selected pipe or vertex the document no longer has.
	$effect(() => {
		const i = ed.selectedPipe;
		const v = ed.selectedVertex;
		const pipe = i !== null ? ed.doc?.pipes?.[i] : undefined;
		if (i !== null && !pipe) selectPipe(null);
		else if (v !== null && !(pipe?.points ?? [])[v]) ed.selectedVertex = null;
	});

	function inField(e: KeyboardEvent): boolean {
		const t = e.target as HTMLElement | null;
		return !!t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
	}
	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			if (inField(e)) {
				(e.target as HTMLElement).blur();
				return;
			}
			if (ed.tool === 'place' || ed.pipeFrom) disarm();
			else if (ed.selectedVertex !== null) ed.selectedVertex = null;
			else if (ed.selectedPipe !== null) selectPipe(null);
			else ed.selected = null;
			return;
		}
		if (inField(e)) return;
		if ((e.key === 'Delete' || e.key === 'Backspace') && ed.selected) {
			e.preventDefault();
			postOp({ type: 'deleteNode', id: ed.selected });
			ed.selected = null;
		} else if ((e.key === 'Delete' || e.key === 'Backspace') && ed.selectedPipe !== null) {
			e.preventDefault();
			if (ed.selectedVertex === null || !deleteSelectedVertex()) {
				postOp({ type: 'deletePipe', index: ed.selectedPipe });
				selectPipe(null);
			}
		} else if (e.key === 'g' || e.key === 'G') ed.gizmo = 'translate';
		else if (e.key === 'r' || e.key === 'R') ed.gizmo = 'rotate';
	}

	const missing = $derived(missingAssets());
	/** Kind files that did not inline: not found, or not a kind. */
	const badKindFiles = $derived(ed.kindFiles.filter((f) => !f.file || f.error));
	const hint = $derived(
		ed.tool === 'place'
			? `click the ground to place a ${ed.placeKind} · Shift = off-grid · Esc cancels`
			: ed.pipeFrom
				? `pipe from ${ed.pipeFrom.node}.${ed.pipeFrom.port} · click another port to run it there · Esc cancels`
				: ed.selectedVertex !== null
					? `drag the gizmo to move the point · Shift = off-grid · Del removes the point · Esc`
					: ed.selectedPipe !== null
						? `pipe ${ed.doc?.pipes?.[ed.selectedPipe]?.id ?? '#' + ed.selectedPipe}: click a point on the stage to move it · Del deletes the pipe · Esc`
						: ed.selected
				? `drag the gizmo to move (G) or turn (R) · Shift = off-grid · Del deletes · Esc deselects`
				: 'click a node to select it · click a port, then another, to run a pipe · drag to orbit, right-drag to pan, wheel to zoom · pick a kind on the left to place one'
	);
</script>

<svelte:window onkeydown={onKey} />

<div class="editor">
	<header>
		<span class="title">{ed.title}</span>
		{#if ed.doc?.name}<span class="name">· {ed.doc.name}</span>{/if}
		<span class="tools" role="toolbar" aria-label="Gizmo">
			<button class:active={ed.gizmo === 'translate'} onclick={() => (ed.gizmo = 'translate')} title="Move the selected node (G)">Move</button>
			<button class:active={ed.gizmo === 'rotate'} onclick={() => (ed.gizmo = 'rotate')} title="Turn the selected node (R)">Rotate</button>
		</span>
		<button
			class="snap-toggle"
			class:active={ed.snapToGrid}
			onclick={() => setSnapToGrid(!ed.snapToGrid)}
			title="Snap a dragged node to the grid cell and its rotation to 15°. Hold Shift while dragging for a free move."
		>
			Snap: {ed.snapToGrid ? 'On' : 'Off'}
		</button>
		<span class="tools" role="toolbar" aria-label="Look">
			{#each LOOKS as l (l)}
				<button class:active={stageLook() === l} onclick={() => (ed.look = l)} title={LOOK_TIP[l]}>{l[0].toUpperCase() + l.slice(1)}</button>
			{/each}
		</span>
		<span class="spacer"></span>
		<button
			class="live nx-pill"
			class:off={!ed.liveEnabled || !ed.tags}
			onclick={toggleLive}
			title={!ed.liveEnabled
				? 'Live values are off — click to enable'
				: ed.tags
					? 'Live tags from nautilus.runtimeUrl — bound nodes animate with the process. Click to disable'
					: "Live values enabled but the controller isn't answering — is it running? Click to disable"}
		>
			{!ed.liveEnabled ? '○ live off' : ed.tags ? '● live' : '◌ offline'}
		</button>
	</header>

	{#if ed.error && !ed.doc}
		<div class="fatal" role="alert">
			<p>This file isn't a scene document the editor can open:</p>
			<pre>{ed.error}</pre>
			<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
		</div>
	{:else}
		{#if ed.error}
			<div class="err" role="alert">
				<span>JSON: {ed.error} — the scene is read-only until the text parses again</span>
				<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
			</div>
		{/if}
		{#if badKindFiles.length}
			<div class="warn" role="status">
				<span>
					{#each badKindFiles as f, i (f.name)}{i ? ' · ' : ''}<b>{f.name}</b>: {f.error ? `${f.path} — ${f.error}` : `${f.path} not found next to the scene file`}{/each}
					— {badKindFiles.length === 1 ? 'the kind draws' : 'those kinds draw'} as a grey box until the file is right.
				</span>
			</div>
		{/if}
		{#if missing.length}
			<div class="warn" role="status">
				<span>
					Not found: {missing.map((a) => a.path).join(', ')} — looked next to the scene file, under hmi/static/ and hmi/build/. The
					{missing.length === 1 ? 'kind draws' : 'kinds draw'} as a grey box until the file is there.
				</span>
			</div>
		{/if}
		<div class="cols" class:locked={!!ed.error} inert={!!ed.error}>
			<KindPalette />
			<Stage3D />
			<NodePanel />
		</div>
	{/if}

	<footer title={hint}>{hint}</footer>
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
	.tools button,
	.snap-toggle {
		padding: 3px 10px;
		border: 1px solid transparent;
		border-radius: 4px;
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
	}
	.snap-toggle {
		border-color: var(--nx-border);
	}
	.tools button:hover,
	.snap-toggle:hover {
		background: var(--nx-hover);
	}
	.tools button.active,
	.snap-toggle.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.spacer {
		flex: 1;
	}
	.live {
		cursor: pointer;
		font-size: 12px;
	}
	.live.off {
		opacity: 0.6;
	}
	.err,
	.warn {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		font-size: 12px;
		border-bottom: 1px solid var(--nx-border);
	}
	.err {
		background: var(--nx-err-bg);
		color: var(--nx-err);
	}
	.warn {
		color: var(--nx-warn);
	}
	.err span,
	.warn span {
		flex: 1;
	}
	.reopen {
		padding: 3px 10px;
		border: 1px solid var(--nx-border);
		border-radius: 4px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
	}
	.fatal {
		padding: 20px;
		color: var(--nx-ui-ink);
	}
	.fatal pre {
		font-family: var(--nx-mono);
		color: var(--nx-err);
		white-space: pre-wrap;
	}
	.cols {
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.cols.locked {
		opacity: 0.6;
	}
	footer {
		flex: none;
		padding: 4px 10px;
		font-size: 11px;
		color: var(--nx-muted);
		border-top: 1px solid var(--nx-border);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
