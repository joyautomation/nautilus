<script lang="ts">
	// The FBD diagram editor: renders the Go model on Svelte Flow, and turns
	// every gesture into a structural op the extension pipes through
	// `nautilus fbd edit`. Layout is auto (banded, from topology) except for
	// nodes the user has dragged — those pin to the (* @layout *) block via
	// setLayout ops. Diff mode overlays two models read-only.
	import {
		SvelteFlow,
		Background,
		MiniMap,
		Controls,
		type Node,
		type Edge,
		type Connection
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import FbdNode from './FbdNode.svelte';
	import FbdEdge from './FbdEdge.svelte';
	import FitController from './FitController.svelte';
	import Palette from './Palette.svelte';
	import VarsPanel from './VarsPanel.svelte';
	import { layout, type FbdModel, type VarDecl } from './layout';
	import { mergeDiff } from './diff';
	import { vscode, postOp } from './vscodeApi';
	import { setRects, updateRect } from './diagState.svelte';
	import { live, setLive } from './liveState.svelte';

	const nodeTypes = { fbd: FbdNode };
	const edgeTypes = { fbd: FbdEdge };

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);
	let title = $state('FBD');
	let hint = $state(true);
	let diffing = $state(false);
	let error = $state('');
	let structureKey = $state('');
	let paletteOpen = $state(false);
	let varsOpen = $state(false);
	let varList = $state<VarDecl[]>([]);
	let usedNames = $state(new Set<string>());
	let hasPins = $state(false);
	let selectedCount = $state(0);
	let knownIds = new Set<string>();
	type Diag = { line: number; message: string; severity: 'error' | 'warning' };
	let diags: Diag[] = [];
	let problemCount = $state(0);
	let problemTip = $state('');
	let lastModel: FbdModel | null = null;

	// Floating text input for constants/renames.
	let input = $state<{ at: { x: number; y: number; w: number }; value: string; commit: (v: string) => void } | null>(null);
	let inputEl = $state<HTMLInputElement | null>(null);
	$effect(() => {
		if (input && inputEl) {
			inputEl.focus();
			inputEl.select();
		}
	});

	function requestInput(init: string, at: { x: number; y: number; w: number }, commit: (v: string) => void) {
		input = { at, value: init, commit };
	}
	function inputKeydown(ev: KeyboardEvent) {
		ev.stopPropagation();
		if (ev.key === 'Enter' && input) {
			const v = input.value.trim();
			const init = input.commit;
			input = null;
			if (v) init(v);
		} else if (ev.key === 'Escape') {
			input = null;
		}
	}

	function render(model: FbdModel, isDiff: boolean) {
		if (!isDiff) lastModel = model;
		const { placed, edges: modelEdges, laneIdx } = layout(model);
		const editable = !isDiff;
		// Join the compiler's squiggles onto nodes by source line — the same
		// message the text editor shows, as a badge + tooltip on the block.
		const diagsByLine = new Map<number, Diag[]>();
		if (!isDiff) {
			for (const d of diags) {
				(diagsByLine.get(d.line) ?? diagsByLine.set(d.line, []).get(d.line)!).push(d);
			}
		}
		problemCount = diags.length;
		problemTip = diags.map((d) => `line ${d.line}: ${d.message}`).join('\n');
		hasPins = model.nodes.some((n) => n.x !== undefined && n.y !== undefined);
		if (!isDiff) {
			varList = model.vars ?? [];
			// Referenced = it became a diagram element (chip, coil, FB instance).
			usedNames = new Set(
				model.nodes
					.filter((n) => n.kind === 'input' || n.kind === 'coil' || n.kind === 'fb')
					.map((n) => n.label.split('.')[0].toLowerCase())
			);
		}
		nodes = placed.map((n) => ({
			id: n.id,
			type: 'fbd',
			position: { x: n.x, y: n.y },
			data: {
				n,
				problems: n.line ? (diagsByLine.get(n.line) ?? []) : [],
				editable,
				requestInput,
				onEdit: (a: { type: 'setLiteral' | 'rename' | 'declareVar'; node: string; value: string }) => {
					if (a.type === 'setLiteral') postOp({ type: 'setLiteral', node: a.node, value: a.value });
					else if (a.type === 'rename') postOp({ type: 'rename', node: a.node, newName: a.value });
					// declareVar: node carries the variable NAME, value its type.
					else postOp({ type: 'declareVar', newName: a.node, value: a.value, text: 'VAR_EXTERNAL' });
				}
			},
			draggable: editable,
			connectable: editable,
			selectable: true,
			deletable: editable && (n.id.startsWith('b:w.') || n.id.startsWith('c:') || n.id.startsWith('f:'))
		}));
		const srcWire = new Map(placed.map((n) => [n.id, !!n.wire]));
		edges = modelEdges.map((e, i) => ({
			id: `${e.from}|${e.fromPin ?? ''}|${e.to}|${e.toPin ?? ''}|${i}`,
			type: 'fbd',
			source: e.from,
			sourceHandle: e.fromPin ?? '',
			target: e.to,
			targetHandle: e.toPin ?? '',
			selectable: editable,
			data: {
				e,
				lane: laneIdx.get(e),
				editable,
				showWireLabel: !!e.wire && !srcWire.get(e.from)
			}
		}));
		setRects(placed.map((n) => [n.id, { x: n.x, y: n.y, w: n.w, h: n.h }]));
		knownIds = new Set(placed.map((n) => n.id));
		structureKey = placed
			.map((n) => n.id)
			.sort()
			.join('|');
		diffing = isDiff;
	}

	type Msg =
		| { type: 'model'; model: FbdModel; title?: string }
		| { type: 'diff'; base: FbdModel; head: FbdModel; title?: string }
		| { type: 'error'; message: string; title?: string };

	function show(msg: Msg) {
		if (msg.type === 'model') {
			title = (msg.model.name ? msg.model.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			render(msg.model, false);
		} else if (msg.type === 'diff') {
			title = (msg.head.name ? msg.head.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			render(mergeDiff(msg.base, msg.head), true);
		} else {
			title = msg.title ?? title;
			error = msg.message;
		}
	}

	window.addEventListener('message', (ev) => {
		const msg = ev.data as Msg & {
			type: 'diagnostics' | 'liveValues';
			diags?: Diag[];
			enabled?: boolean;
			fresh?: boolean;
			values?: Record<string, unknown>;
		};
		if (!msg?.type) return;
		if (msg.type === 'liveValues') {
			// Store-only update: FbdNode pills react directly, no node rebuild.
			setLive({ enabled: !!msg.enabled, fresh: !!msg.fresh, values: msg.values ?? {} });
			return;
		}
		if (msg.type === 'diagnostics') {
			diags = msg.diags ?? [];
			if (!diffing && lastModel) render(lastModel, false);
			return;
		}
		if (msg.type !== 'error') vscode.setState(msg);
		show(msg);
	});
	const saved = vscode.getState() as Msg | null;
	if (saved) show(saved);
	const injected = window.__MODEL__ as FbdModel | undefined;
	if (injected) show({ type: 'model', model: injected, title: 'harness' });

	// ── gestures → ops ──────────────────────────────────────────────────────
	function onconnect(c: Connection) {
		// Dragging output→input rewires that input (or wires an unwired FB
		// pin); dropping on an extensible block's "+" pin appends an input.
		// We never mutate edges locally: the op round-trips through the text
		// and the re-render brings the new wiring back.
		if (c.targetHandle === '+') {
			postOp({
				type: 'addInput',
				node: c.target,
				source: c.source,
				sourcePin: c.sourceHandle ?? ''
			});
			return;
		}
		postOp({
			type: 'rewire',
			to: c.target,
			toPin: c.targetHandle ?? '',
			source: c.source,
			sourcePin: c.sourceHandle ?? ''
		});
	}

	function onbeforedelete({ nodes: sel, edges: selEdges }: { nodes: Node[]; edges: Edge[] }) {
		for (const n of sel) postOp({ type: 'deleteNode', node: n.id });
		// A selected edge deletes as a DISCONNECT: FB pins drop their named
		// arg, extensible inputs shrink, fixed-arity/coil inputs explain.
		for (const e of selEdges) {
			const d = e.data as { e?: { to: string; toPin?: string; from: string; fromPin?: string } };
			if (!d?.e) continue;
			postOp({
				type: 'disconnect',
				to: d.e.to,
				toPin: d.e.toPin ?? '',
				from: d.e.from,
				fromPin: d.e.fromPin ?? ''
			});
		}
		return Promise.resolve(false); // ops re-render; never delete locally
	}

	function trackDrag(dragged: Node[]) {
		// Keep the live rect store current so feedback lanes reroute around
		// nodes as they move. Selection drags can hand us synthetic group
		// entries — only track nodes we actually rendered.
		for (const n of dragged) {
			if (!n?.id || !knownIds.has(n.id)) continue;
			const pn = (n.data as { n?: import('./layout').Placed })?.n;
			if (!pn) continue;
			updateRect(n.id, { x: n.position.x, y: n.position.y, w: pn.w, h: pn.h });
		}
	}

	function onnodedrag({ nodes: dragged }: { nodes: Node[] }) {
		trackDrag(dragged);
	}

	function onnodedragstop({ nodes: dragged }: { nodes: Node[] }) {
		trackDrag(dragged);
		// ONE batched op for the whole selection — per-node ops would race
		// each other rewriting the layout block and drop all but the last.
		// Selection drags include synthetic group entries; pin only the nodes
		// that exist in the model.
		const entries = dragged
			.filter((n) => n?.id && knownIds.has(n.id))
			.map((n) => ({
				node: n.id,
				x: Math.round(n.position.x),
				y: Math.round(n.position.y)
			}));
		if (entries.length === 0) return;
		postOp({ type: 'setLayout', entries });
	}

	function onselectionchange({ nodes: sel }: { nodes: Node[]; edges: Edge[] }) {
		selectedCount = sel.length;
	}
</script>

<div class="host" class:diffing>
	<div class="bar">
		<span class="title">{title}</span>
		{#if diffing}
			<span class="legend">
				<span><i class="sw added"></i>added</span>
				<span><i class="sw removed"></i>removed</span>
				<span><i class="sw changed"></i>changed</span>
			</span>
		{:else if hint}
			<span class="hint">double-click: edit & rename · drag pin→pin: wire (+ adds an input) · drag node: pin layout · Del: delete / disconnect</span>
		{/if}
		<span class="spacer"></span>
		{#if problemCount > 0 && !diffing}
			<span class="problems" title={problemTip}>{problemCount} problem{problemCount === 1 ? '' : 's'}</span>
		{/if}
		{#if selectedCount > 0}
			<span class="selcount">{selectedCount} selected</span>
		{/if}
		{#if !diffing}
			{#if live.seen}
				<!-- same toggle as the text editor's status bar item -->
				<button
					class="livepill"
					class:on={live.enabled && live.fresh}
					class:offline={live.enabled && !live.fresh}
					title={live.enabled
						? live.fresh
							? 'Streaming live values from the controller — click to disable'
							: 'Live values enabled but no frames arriving — is the controller running? Click to disable'
						: 'Live values are off — click to enable'}
					onclick={() => vscode.postMessage({ type: 'toggleLive' })}
				>{live.enabled ? (live.fresh ? '● live' : '◌ offline') : '○ live off'}</button>
			{/if}
			{#if hasPins}
				<button title="Clear all pinned positions (back to full auto-layout)" onclick={() => postOp({ type: 'clearLayout' })}>auto layout</button>
			{/if}
			<button title="All header declarations, including ones the logic doesn't reference yet" onclick={(e) => { e.stopPropagation(); varsOpen = !varsOpen; paletteOpen = false; }}>vars</button>
			<button title="Insert an instruction" onclick={(e) => { e.stopPropagation(); paletteOpen = !paletteOpen; varsOpen = false; }}>+ add</button>
		{/if}
	</div>
	{#if error}
		<div class="error">{error}</div>
	{/if}
	<div class="flow" class:stale={!!error}>
		<SvelteFlow
			bind:nodes
			bind:edges
			{nodeTypes}
			{edgeTypes}
			{onconnect}
			{onbeforedelete}
			{onnodedrag}
			{onnodedragstop}
			{onselectionchange}
			zoomOnDoubleClick={false}
			fitView
			minZoom={0.15}
			deleteKey={['Delete', 'Backspace']}
			proOptions={{ hideAttribution: true }}
		>
			<Background />
			<MiniMap pannable zoomable />
			<Controls />
			<FitController {structureKey} />
		</SvelteFlow>
	</div>
	<Palette bind:open={paletteOpen} />
	<VarsPanel bind:open={varsOpen} vars={varList} used={usedNames} />
	{#if input}
		<input
			bind:this={inputEl}
			class="float-input"
			spellcheck="false"
			style="left: {input.at.x}px; top: {input.at.y - 2}px; width: {Math.max(input.at.w, 72)}px"
			bind:value={input.value}
			onkeydown={inputKeydown}
			onblur={() => (input = null)}
		/>
	{/if}
</div>

<svelte:window onclick={() => { paletteOpen = false; varsOpen = false; }} />

<style>
	.host {
		height: 100vh;
		display: flex;
		flex-direction: column;
		background: var(--vscode-editor-background, #1e1e1e);
		color: var(--vscode-foreground, #ccc);
		font-family: var(--vscode-font-family, sans-serif);
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 4px 10px;
		font-size: 12px;
		border-bottom: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.35));
		user-select: none;
	}
	.bar .title {
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.bar .hint {
		font-size: 11px;
		color: var(--vscode-descriptionForeground, #888);
	}
	.bar .spacer {
		flex: 1;
	}
	.bar button {
		background: transparent;
		color: var(--vscode-foreground, #ccc);
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.35));
		border-radius: 3px;
		padding: 1px 8px;
		height: 22px;
		cursor: pointer;
		font-size: 12px;
	}
	.bar button:hover {
		background: var(--vscode-toolbar-hoverBackground, rgba(128, 128, 128, 0.2));
	}
	.bar button.livepill {
		border-radius: 999px;
		color: var(--vscode-descriptionForeground, #8c8c8c);
	}
	.bar button.livepill.on {
		color: var(--vscode-charts-green, #64d88a);
		border-color: var(--vscode-charts-green, #64d88a);
	}
	.bar button.livepill.offline {
		color: var(--vscode-editorWarning-foreground, #d7a021);
		border-color: var(--vscode-editorWarning-foreground, #d7a021);
	}
	.legend {
		display: inline-flex;
		gap: 10px;
		font-size: 11px;
	}
	.legend .sw {
		display: inline-block;
		width: 10px;
		height: 10px;
		border-radius: 2px;
		margin-right: 3px;
		vertical-align: -1px;
	}
	.legend .sw.added {
		background: var(--vscode-gitDecoration-addedResourceForeground, #2ea043);
	}
	.legend .sw.removed {
		background: var(--vscode-gitDecoration-deletedResourceForeground, #f85149);
	}
	.legend .sw.changed {
		background: var(--vscode-gitDecoration-modifiedResourceForeground, #d7a021);
	}
	.selcount {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		color: var(--vscode-focusBorder, #58a6ff);
		border: 1px solid var(--vscode-focusBorder, #58a6ff);
	}
	.problems {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		color: var(--vscode-errorForeground, #f48771);
		border: 1px solid var(--vscode-errorForeground, #f48771);
		cursor: help;
	}
	.error {
		padding: 6px 10px;
		font-size: 12px;
		white-space: pre-wrap;
		font-family: var(--vscode-editor-font-family, monospace);
		color: var(--vscode-errorForeground, #f48771);
		background: var(--vscode-inputValidation-errorBackground, rgba(200, 60, 60, 0.12));
		border-bottom: 1px solid var(--vscode-errorForeground, #f48771);
	}
	.flow {
		flex: 1;
		min-height: 0;
	}
	.flow.stale {
		opacity: 0.45;
	}
	.diffing :global(.fbd-edge.same),
	.diffing :global(.svelte-flow__node:has(.same)) {
		opacity: 0.6;
	}
	.float-input {
		position: fixed;
		z-index: 30;
		font-family: var(--vscode-editor-font-family, monospace);
		font-size: 12px;
		padding: 2px 6px;
		border-radius: 4px;
		background: var(--vscode-input-background, #3c3c3c);
		color: var(--vscode-input-foreground, #ccc);
		border: 1px solid var(--vscode-focusBorder, #58a6ff);
		outline: none;
	}
	:global(.svelte-flow) {
		background: var(--vscode-editor-background, #1e1e1e) !important;
	}
	:global(.svelte-flow__minimap) {
		background: var(--vscode-editorWidget-background, #252526) !important;
	}
	:global(.svelte-flow__controls button) {
		background: var(--vscode-editorWidget-background, #252526);
		border-bottom: 1px solid var(--vscode-editorWidget-border, #454545);
		fill: var(--vscode-foreground, #ccc);
	}
	:global(.svelte-flow__edge.selected .wirepath) {
		stroke: var(--vscode-focusBorder, #58a6ff) !important;
		stroke-width: 2.4;
	}
	:global(.svelte-flow__connectionline path) {
		stroke: var(--vscode-charts-blue, #58a6ff);
		stroke-dasharray: 5 3;
	}
</style>
