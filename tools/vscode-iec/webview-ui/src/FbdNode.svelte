<script lang="ts">
	// One FBD element as a Svelte Flow node: block/fb boxes with a pin handle
	// per input/output, input/coil chips with a single implicit handle. Handle
	// ids are the PIN NAMES, so connections address sourceHandle/targetHandle
	// exactly like the Go model's fromPin/toPin. Double-click edits: constants
	// retype (setLiteral), FB instances and named wires rename.
	import { Handle, Position } from '@xyflow/svelte';
	import type { Placed } from './layout';
	import { pinOffset, EXTENSIBLE } from './layout';

	let {
		data
	}: {
		data: {
			n: Placed;
			problems?: { message: string; severity: string }[];
			editable: boolean;
			requestInput: (init: string, at: { x: number; y: number; w: number }, commit: (v: string) => void) => void;
			onEdit: (op: { type: 'setLiteral' | 'rename' | 'declareVar'; node: string; value: string }) => void;
		};
	} = $props();
	const n = $derived(data.n);
	const chip = $derived(n.kind === 'input' || n.kind === 'coil');
	const editableConst = $derived(data.editable && !!n.src);
	const renameable = $derived(data.editable && (n.kind === 'fb' || (n.kind === 'block' && !!n.wire)));
	const plusPin = $derived(data.editable && n.kind === 'block' && EXTENSIBLE.has(n.label));
	const plusTop = $derived(n.titleH + (n.ins.length + 0.5) * 18);

	// A direct listener (not Svelte's delegated ondblclick): survives synthetic
	// events in tests and never races xyflow's node wrapper handling.
	function dblEdit(el: HTMLElement) {
		const handler = (ev: MouseEvent) => {
			ev.stopPropagation();
			const rect = el.getBoundingClientRect();
			const at = { x: rect.left, y: rect.top, w: rect.width };
			if (undeclared) {
				data.requestInput('REAL', at, (v) => data.onEdit({ type: 'declareVar', node: n.label, value: v }));
			} else if (editableConst) {
				data.requestInput(n.label, at, (v) => data.onEdit({ type: 'setLiteral', node: n.id, value: v }));
			} else if (renameable) {
				const current = n.kind === 'fb' ? n.label : (n.wire ?? '');
				data.requestInput(current, at, (v) => data.onEdit({ type: 'rename', node: n.id, value: v }));
			}
		};
		el.addEventListener('dblclick', handler);
		return { destroy: () => el.removeEventListener('dblclick', handler) };
	}

	const problems = $derived(data.problems ?? []);
	// An undeclared variable chip offers declare-in-place: double-click, type
	// the type, done — the header edit the netlist itself can't express.
	const undeclared = $derived(
		data.editable &&
			n.kind === 'input' &&
			!n.src &&
			problems.some((p) => /undeclared/i.test(p.message))
	);
	const title = $derived.by(() => {
		// Diagnostics take over the tooltip — the same message as the squiggle.
		if (undeclared) {
			return problems.map((p) => p.message).join('\n') + '\ndouble-click to declare (enter its type)';
		}
		if (problems.length) return problems.map((p) => p.message).join('\n');
		const base = n.kind === 'fb' ? `${n.label} : ${n.type ?? '?'}` : n.label;
		if (editableConst) return `${base} — double-click to edit`;
		if (renameable) return `${base} — double-click to rename`;
		return base;
	});
</script>

{#if chip}
	<div
		class="chip {n.kind} {n.status ?? ''}"
		class:editable={editableConst || undeclared}
		class:problem={problems.length > 0}
		style="width: {n.w}px; height: {n.h}px"
		{title}
		use:dblEdit
	>
		{#if n.kind === 'coil'}
			<Handle type="target" position={Position.Left} id="" style="top: {n.h / 2}px" isConnectable={data.editable} />
		{/if}
		<span>{n.label}</span>
		<Handle type="source" position={Position.Right} id="" style="top: {n.h / 2}px" isConnectable={data.editable} />
		{#if problems.length}<span class="badge">!</span>{/if}
	</div>
{:else}
	<div
		class="block {n.status ?? ''}"
		class:editable={renameable}
		class:problem={problems.length > 0}
		style="width: {n.w}px; height: {n.h}px"
		{title}
		use:dblEdit
	>
		<div class="title" style="height: {n.titleH}px">
			<span class="name">{n.label}</span>
			{#if n.kind === 'fb'}<span class="type">{n.type ?? '?'}</span>{/if}
		</div>
		{#each n.ins as pin (pin)}
			<Handle type="target" position={Position.Left} id={pin} style="top: {pinOffset(n, pin, 'in')}px" isConnectable={data.editable} />
			<span class="pin in" style="top: {pinOffset(n, pin, 'in') - 7}px">{pin}</span>
		{/each}
		{#each n.outs as pin (pin)}
			<Handle type="source" position={Position.Right} id={pin} style="top: {pinOffset(n, pin, 'out')}px" isConnectable={data.editable} />
			<span class="pin out" style="top: {pinOffset(n, pin, 'out') - 7}px">{pin}</span>
		{/each}
		{#if plusPin}
			<!-- drop a wire here to ADD an input: the pin exists because it's wired -->
			<Handle type="target" class="plus-handle" position={Position.Left} id="+" style="top: {plusTop}px" isConnectable={true} />
			<span class="pin in plus" style="top: {plusTop - 7}px" title="drop a wire here to add an input">+</span>
		{/if}
		{#if n.wire}<span class="wire">{n.wire}</span>{/if}
		{#if problems.length}<span class="badge">!</span>{/if}
	</div>
{/if}

<style>
	.chip,
	.block {
		--ink: var(--vscode-editor-foreground, #d4d4d4);
		box-sizing: border-box;
		background: var(--vscode-editorWidget-background, #252526);
		border: 1.2px solid var(--ink);
		color: var(--ink);
		font-family: var(--vscode-editor-font-family, monospace);
		font-size: 12px;
		position: relative;
	}
	.added {
		--ink: var(--vscode-gitDecoration-addedResourceForeground, #2ea043);
	}
	.removed {
		--ink: var(--vscode-gitDecoration-deletedResourceForeground, #f85149);
		border-style: dashed;
		opacity: 0.85;
	}
	.changed {
		--ink: var(--vscode-gitDecoration-modifiedResourceForeground, #d7a021);
	}
	/* A diagnostic on this element's line: red border + badge; the tooltip
	   carries the compiler's message, exactly like the squiggle's hover. */
	.problem {
		border-color: var(--vscode-errorForeground, #f48771) !important;
		box-shadow: 0 0 8px color-mix(in srgb, var(--vscode-errorForeground, #f48771) 45%, transparent);
	}
	.badge {
		position: absolute;
		top: -7px;
		right: -7px;
		width: 14px;
		height: 14px;
		border-radius: 50%;
		background: var(--vscode-errorForeground, #f48771);
		color: var(--vscode-editor-background, #1e1e1e);
		font-size: 10px;
		font-weight: 800;
		line-height: 14px;
		text-align: center;
		pointer-events: none;
	}
	.editable {
		cursor: pointer;
	}
	.editable:hover {
		border-width: 2px;
	}
	.chip {
		border-radius: 12px;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.block {
		border-radius: 2px;
	}
	.title {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		border-bottom: 0.6px solid color-mix(in srgb, var(--ink) 50%, transparent);
	}
	.title .type {
		font-size: 10px;
		opacity: 0.75;
	}
	.pin {
		position: absolute;
		font-size: 9px;
		opacity: 0.8;
		pointer-events: none;
	}
	.pin.in {
		left: 4px;
	}
	.pin.out {
		right: 4px;
	}
	.wire {
		position: absolute;
		right: -6px;
		top: 8px;
		transform: translateX(100%);
		font-size: 9px;
		opacity: 0.85;
	}
	:global(.svelte-flow__handle) {
		width: 7px;
		height: 7px;
		background: var(--vscode-charts-blue, #58a6ff);
		border: none;
		opacity: 0.65;
	}
	:global(.svelte-flow__handle.plus-handle) {
		background: transparent;
		border: 1.2px dashed var(--vscode-charts-blue, #58a6ff);
		width: 9px;
		height: 9px;
	}
	.pin.plus {
		color: var(--vscode-charts-blue, #58a6ff);
		opacity: 0.9;
		font-weight: 700;
	}
	/* Selection must be unmissable, single node included: the wrapper gets
	   an outline + glow, AND the element itself recolors (multiple cues so
	   no theme/zoom combination can hide it). */
	:global(.svelte-flow__node.selected) {
		outline: 2px solid var(--vscode-focusBorder, #58a6ff);
		outline-offset: 3px;
		border-radius: 4px;
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--vscode-focusBorder, #58a6ff) 25%, transparent),
			0 0 14px color-mix(in srgb, var(--vscode-focusBorder, #58a6ff) 45%, transparent);
	}
	:global(.svelte-flow__node.selected) .chip,
	:global(.svelte-flow__node.selected) .block {
		border-color: var(--vscode-focusBorder, #58a6ff);
		border-width: 2px;
		background: color-mix(
			in srgb,
			var(--vscode-focusBorder, #58a6ff) 14%,
			var(--vscode-editorWidget-background, #252526)
		);
	}
</style>
