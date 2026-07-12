<script lang="ts">
	// FBD wire: bezier for forward connections, precomputed lane polyline for
	// backward/feedback wires, IEC negation circle at the input pin (click to
	// toggle NOT), and the signal name labelled at the source. Diff status
	// colors ride a class on the group.
	import { getBezierPath, type EdgeProps } from '@xyflow/svelte';
	import { postOp } from './vscodeApi';

	let {
		sourceX,
		sourceY,
		targetX,
		targetY,
		sourcePosition,
		targetPosition,
		data
	}: EdgeProps = $props();

	const d = $derived(data as {
		e: {
			from: string;
			fromPin?: string;
			to: string;
			toPin?: string;
			wire?: string;
			negated?: boolean;
			feedback?: boolean;
			status?: string;
		};
		lane?: number;
		editable: boolean;
		showWireLabel: boolean;
	});

	// The path derives from the LIVE endpoints xyflow supplies, so wires —
	// including feedback lanes — follow node drags. Forward runs are bezier;
	// backward runs route orthogonally below the lower endpoint, staggered by
	// the layout's lane index so parallel lanes never overlap.
	const backward = $derived(sourceX >= targetX - 4);
	const path = $derived.by(() => {
		const endX = d.e.negated ? targetX - 9 : targetX;
		if (!backward) {
			const [p] = getBezierPath({
				sourceX,
				sourceY,
				targetX: endX,
				targetY,
				sourcePosition,
				targetPosition
			});
			return p;
		}
		const lane = d.lane ?? 0;
		const ly = Math.max(sourceY, targetY) + 26 + lane * 10;
		const ox = sourceX + 14 + lane * 6;
		const ix = endX - 14 - lane * 6;
		return `M ${sourceX} ${sourceY} L ${ox} ${sourceY} L ${ox} ${ly} L ${ix} ${ly} L ${ix} ${targetY} L ${endX} ${targetY}`;
	});

	function toggleNot(ev: MouseEvent) {
		ev.stopPropagation();
		if (!d.editable) return;
		postOp({
			type: 'toggleNot',
			to: d.e.to,
			toPin: d.e.toPin ?? '',
			from: d.e.from,
			fromPin: d.e.fromPin ?? ''
		});
	}
</script>

<g class="fbd-edge {d.e.status ?? ''}" class:feedback={d.e.feedback || backward}>
	<path class="wirepath" d={path} fill="none" />
	{#if d.e.negated}
		<circle class="neg" cx={targetX - 4.5} cy={targetY} r="4.5" />
	{/if}
	{#if d.editable}
		<!-- input-pin hit target: toggle NOT (also highlights as a drop hint) -->
		<circle
			class="not-hit"
			cx={targetX - 4.5}
			cy={targetY}
			r="8"
			role="button"
			tabindex="-1"
			aria-label={d.e.negated ? 'remove NOT' : 'add NOT'}
			onclick={toggleNot}
		>
		</circle>
	{/if}
	{#if d.e.wire && d.showWireLabel && !backward}
		<text class="wirelabel" x={sourceX + 6} y={sourceY - 5}>{d.e.wire}</text>
	{/if}
</g>

<style>
	.fbd-edge {
		--wire: var(--vscode-editor-foreground, #d4d4d4);
	}
	.fbd-edge.added {
		--wire: var(--vscode-gitDecoration-addedResourceForeground, #2ea043);
	}
	.fbd-edge.removed {
		--wire: var(--vscode-gitDecoration-deletedResourceForeground, #f85149);
	}
	.fbd-edge.changed {
		--wire: var(--vscode-gitDecoration-modifiedResourceForeground, #d7a021);
	}
	.wirepath {
		stroke: var(--wire);
		stroke-width: 1.4;
		opacity: 0.9;
	}
	.fbd-edge.feedback .wirepath {
		stroke-dasharray: 6 3;
	}
	.neg {
		fill: var(--vscode-editor-background, #1e1e1e);
		stroke: var(--wire);
		stroke-width: 1.3;
	}
	.not-hit {
		fill: transparent;
		pointer-events: all;
		cursor: pointer;
	}
	.not-hit:hover {
		fill: var(--vscode-editor-foreground, #d4d4d4);
		fill-opacity: 0.18;
	}
	.wirelabel {
		font-size: 9px;
		fill: var(--wire);
		opacity: 0.85;
		font-family: var(--vscode-editor-font-family, monospace);
	}
</style>
