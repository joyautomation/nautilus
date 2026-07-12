<script lang="ts">
	// One FBD element as a Svelte Flow node: block/fb boxes with a pin handle
	// per input/output, input/coil chips with a single implicit handle. The
	// handle ids are the PIN NAMES, so edges address sourceHandle/targetHandle
	// exactly like the Go model's fromPin/toPin.
	import { Handle, Position } from '@xyflow/svelte';
	import type { Placed } from './layout';
	import { pinOffset } from './layout';

	let { data }: { data: { n: Placed } } = $props();
	const n = $derived(data.n);
	const chip = $derived(n.kind === 'input' || n.kind === 'coil');
</script>

{#if chip}
	<div class="chip {n.kind}" style="width: {n.w}px; height: {n.h}px">
		{#if n.kind === 'coil'}
			<Handle type="target" position={Position.Left} id="" style="top: {n.h / 2}px" />
		{/if}
		<span>{n.label}</span>
		<Handle type="source" position={Position.Right} id="" style="top: {n.h / 2}px" />
	</div>
{:else}
	<div class="block" style="width: {n.w}px; height: {n.h}px">
		<div class="title" style="height: {n.titleH}px">
			<span class="name">{n.label}</span>
			{#if n.kind === 'fb'}<span class="type">{n.type ?? '?'}</span>{/if}
		</div>
		{#each n.ins as pin (pin)}
			<Handle type="target" position={Position.Left} id={pin} style="top: {pinOffset(n, pin, 'in')}px" />
			<span class="pin in" style="top: {pinOffset(n, pin, 'in') - 7}px">{pin}</span>
		{/each}
		{#each n.outs as pin (pin)}
			<Handle type="source" position={Position.Right} id={pin} style="top: {pinOffset(n, pin, 'out')}px" />
			<span class="pin out" style="top: {pinOffset(n, pin, 'out') - 7}px">{pin}</span>
		{/each}
		{#if n.wire}<span class="wire">{n.wire}</span>{/if}
	</div>
{/if}

<style>
	.chip,
	.block {
		box-sizing: border-box;
		background: var(--vscode-editorWidget-background, #252526);
		border: 1.2px solid var(--vscode-editor-foreground, #d4d4d4);
		color: var(--vscode-editor-foreground, #d4d4d4);
		font-family: var(--vscode-editor-font-family, monospace);
		font-size: 12px;
		position: relative;
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
		border-bottom: 0.6px solid color-mix(in srgb, var(--vscode-editor-foreground, #d4d4d4) 50%, transparent);
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
		opacity: 0.7;
	}
</style>
