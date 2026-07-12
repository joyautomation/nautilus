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
	import { layout, type FbdModel } from './layout';
	import { mergeDiff } from './diff';
	import { vscode, postOp } from './vscodeApi';

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
	let hasPins = $state(false);

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
		const { placed, edges: modelEdges, lanePoints } = layout(model);
		const editable = !isDiff;
		hasPins = model.nodes.some((n) => n.x !== undefined && n.y !== undefined);
		nodes = placed.map((n) => ({
			id: n.id,
			type: 'fbd',
			position: { x: n.x, y: n.y },
			data: {
				n,
				editable,
				requestInput,
				onEdit: (a: { type: 'setLiteral' | 'rename'; node: string; value: string }) =>
					a.type === 'setLiteral'
						? postOp({ type: 'setLiteral', node: a.node, value: a.value })
						: postOp({ type: 'rename', node: a.node, newName: a.value })
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
			selectable: false,
			data: {
				e,
				points: lanePoints.get(e),
				editable,
				showWireLabel: !!e.wire && !srcWire.get(e.from)
			}
		}));
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
		const msg = ev.data as Msg;
		if (!msg?.type) return;
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
		// pin). We never mutate edges locally: the op round-trips through the
		// text and the re-render brings the new wiring back.
		postOp({
			type: 'rewire',
			to: c.target,
			toPin: c.targetHandle ?? '',
			source: c.source,
			sourcePin: c.sourceHandle ?? ''
		});
	}

	function onbeforedelete({ nodes: sel }: { nodes: Node[]; edges: Edge[] }) {
		for (const n of sel) postOp({ type: 'deleteNode', node: n.id });
		return Promise.resolve(false); // ops re-render; never delete locally
	}

	function onnodedragstop({ nodes: dragged }: { nodes: Node[] }) {
		for (const n of dragged) {
			postOp({
				type: 'setLayout',
				node: n.id,
				x: Math.round(n.position.x),
				y: Math.round(n.position.y)
			});
		}
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
			<span class="hint">double-click: edit & rename · drag pin→pin: wire · drag node: pin layout · Del: delete</span>
		{/if}
		<span class="spacer"></span>
		{#if !diffing}
			{#if hasPins}
				<button title="Clear all pinned positions (back to full auto-layout)" onclick={() => postOp({ type: 'clearLayout' })}>auto layout</button>
			{/if}
			<button title="Insert an instruction" onclick={(e) => { e.stopPropagation(); paletteOpen = !paletteOpen; }}>+ add</button>
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
			{onnodedragstop}
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

<svelte:window onclick={() => (paletteOpen = false)} />

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
	:global(.svelte-flow__edge) {
		pointer-events: none;
	}
	:global(.svelte-flow__edge .not-hit) {
		pointer-events: all;
	}
	:global(.svelte-flow__connectionline path) {
		stroke: var(--vscode-charts-blue, #58a6ff);
		stroke-dasharray: 5 3;
	}
</style>
