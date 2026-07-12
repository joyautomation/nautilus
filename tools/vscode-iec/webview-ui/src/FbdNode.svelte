<script lang="ts">
	// One FBD element as a Svelte Flow node: block/fb boxes with a pin handle
	// per input/output, input/coil chips with a single implicit handle. Handle
	// ids are the PIN NAMES, so connections address sourceHandle/targetHandle
	// exactly like the Go model's fromPin/toPin. Double-click edits: constants
	// retype (setLiteral), FB instances and named wires rename.
	import { Handle, Position } from '@xyflow/svelte';
	import type { Placed } from './layout';
	import { pinOffset } from './layout';

	let {
		data
	}: {
		data: {
			n: Placed;
			editable: boolean;
			requestInput: (init: string, at: { x: number; y: number; w: number }, commit: (v: string) => void) => void;
			onEdit: (op: { type: 'setLiteral' | 'rename'; node: string; value: string }) => void;
		};
	} = $props();
	const n = $derived(data.n);
	const chip = $derived(n.kind === 'input' || n.kind === 'coil');
	const editableConst = $derived(data.editable && !!n.src);
	const renameable = $derived(data.editable && (n.kind === 'fb' || (n.kind === 'block' && !!n.wire)));

	// A direct listener (not Svelte's delegated ondblclick): survives synthetic
	// events in tests and never races xyflow's node wrapper handling.
	function dblEdit(el: HTMLElement) {
		const handler = (ev: MouseEvent) => {
			ev.stopPropagation();
			const rect = el.getBoundingClientRect();
			const at = { x: rect.left, y: rect.top, w: rect.width };
			if (editableConst) {
				data.requestInput(n.label, at, (v) => data.onEdit({ type: 'setLiteral', node: n.id, value: v }));
			} else if (renameable) {
				const current = n.kind === 'fb' ? n.label : (n.wire ?? '');
				data.requestInput(current, at, (v) => data.onEdit({ type: 'rename', node: n.id, value: v }));
			}
		};
		el.addEventListener('dblclick', handler);
		return { destroy: () => el.removeEventListener('dblclick', handler) };
	}

	const title = $derived.by(() => {
		const base = n.kind === 'fb' ? `${n.label} : ${n.type ?? '?'}` : n.label;
		if (editableConst) return `${base} — double-click to edit`;
		if (renameable) return `${base} — double-click to rename`;
		return base;
	});
</script>

{#if chip}
	<div
		class="chip {n.kind} {n.status ?? ''}"
		class:editable={editableConst}
		style="width: {n.w}px; height: {n.h}px"
		{title}
		use:dblEdit
	>
		{#if n.kind === 'coil'}
			<Handle type="target" position={Position.Left} id="" style="top: {n.h / 2}px" isConnectable={data.editable} />
		{/if}
		<span>{n.label}</span>
		<Handle type="source" position={Position.Right} id="" style="top: {n.h / 2}px" isConnectable={data.editable} />
	</div>
{:else}
	<div
		class="block {n.status ?? ''}"
		class:editable={renameable}
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
		{#if n.wire}<span class="wire">{n.wire}</span>{/if}
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
	/* Selection must be unmissable, single node included: bright outline +
	   glow on the wrapper (xyflow sets .selected there). */
	:global(.svelte-flow__node.selected) {
		outline: 2px solid var(--vscode-focusBorder, #58a6ff);
		outline-offset: 3px;
		border-radius: 4px;
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--vscode-focusBorder, #58a6ff) 25%, transparent),
			0 0 14px color-mix(in srgb, var(--vscode-focusBorder, #58a6ff) 45%, transparent);
	}
</style>
