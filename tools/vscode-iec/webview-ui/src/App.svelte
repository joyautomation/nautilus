<script lang="ts">
	// Spike: the FBD render model on Svelte Flow inside a webview-compatible
	// bundle. Layout stays ours (computed, non-draggable nodes); xyflow owns
	// viewport, interaction, selection, and connection UX.
	import { SvelteFlow, Background, MiniMap, Controls, type Node, type Edge } from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import FbdNode from './FbdNode.svelte';
	import { layout, type FbdModel } from './layout';

	const nodeTypes = { fbd: FbdNode };

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);
	let title = $state('FBD');

	function setModel(model: FbdModel) {
		const { placed, edges: modelEdges } = layout(model);
		nodes = placed.map((n) => ({
			id: n.id,
			type: 'fbd',
			position: { x: n.x, y: n.y },
			data: { n },
			draggable: false,
			connectable: true,
		}));
		edges = modelEdges.map((e, i) => ({
			id: `e${i}`,
			source: e.from,
			sourceHandle: e.fromPin ?? '',
			target: e.to,
			targetHandle: e.toPin ?? '',
			type: e.feedback ? 'smoothstep' : 'bezier',
			class: e.feedback ? 'feedback' : '',
			label: e.wire || undefined,
			markerEnd: e.negated ? undefined : undefined,
		}));
		title = model.name;
	}

	window.addEventListener('message', (ev) => {
		if (ev.data?.type === 'model') setModel(ev.data.model);
	});
	// Harness hook: model injected before mount.
	const injected = (window as unknown as { __MODEL__?: FbdModel }).__MODEL__;
	if (injected) setModel(injected);
</script>

<div class="host">
	<div class="bar">{title} — Svelte Flow spike</div>
	<SvelteFlow bind:nodes bind:edges {nodeTypes} fitView minZoom={0.2} nodesDraggable={false}>
		<Background />
		<MiniMap />
		<Controls />
	</SvelteFlow>
</div>

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
		padding: 4px 10px;
		font-size: 12px;
		font-weight: 600;
		border-bottom: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.35));
	}
	:global(.svelte-flow) {
		flex: 1;
		background: var(--vscode-editor-background, #1e1e1e) !important;
	}
	:global(.svelte-flow__edge-path) {
		stroke: var(--vscode-editor-foreground, #d4d4d4);
		stroke-width: 1.4;
		opacity: 0.9;
	}
	:global(.svelte-flow__edge.feedback .svelte-flow__edge-path) {
		stroke-dasharray: 6 3;
	}
	:global(.svelte-flow__edge-textwrapper text) {
		fill: var(--vscode-editor-foreground, #d4d4d4);
		font-size: 9px;
	}
</style>
