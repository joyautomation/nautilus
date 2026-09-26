<script lang="ts">
	// One FBD element as a Svelte Flow node: block/fb boxes with a pin handle
	// per input/output, input/coil chips with a single implicit handle. Handle
	// ids are the PIN NAMES, so connections address sourceHandle/targetHandle
	// exactly like the Go model's fromPin/toPin. Double-click edits: constants
	// retype (setLiteral), FB instances and named wires rename.
	import { Handle, Position } from '@xyflow/svelte';
	import type { Placed } from './layout';
	import { pinOffset, EXTENSIBLE, NOTE_LINE_H } from './layout';
	import { live, liveValue, liveMissing, member, formatLive } from './liveState.svelte';

	let {
		data
	}: {
		data: {
			n: Placed;
			problems?: { message: string; severity: string }[];
			editable: boolean;
			extNames?: Set<string>;
			requestInput: (
				init: string,
				at: { x: number; y: number; w: number },
				commit: (v: string) => void,
				opts?: { multiline?: boolean; suggest?: 'tags' | 'types' }
			) => void;
			onEdit: (op: {
				type: 'setLiteral' | 'rename' | 'setComment' | 'retarget';
				node: string;
				value: string;
				declareType?: string;
			}) => void;
			onInspect?: (inst: { name: string; type: string; ins: string[]; outs: string[] }) => void;
		};
	} = $props();
	const n = $derived(data.n);
	const chip = $derived(n.kind === 'input' || n.kind === 'coil');
	const note = $derived(n.kind === 'comment');
	const editableConst = $derived(data.editable && !!n.src);
	// Any variable input chip can be pointed at a different tag in place —
	// how an `_` open pin (or a mis-wired read) picks what it reads.
	const retargetable = $derived(data.editable && n.kind === 'input' && !n.src && !n.ghost);
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
			if (note) {
				// Comments edit in a multiline textarea with real newlines.
				data.requestInput(
					n.label,
					at,
					(v) => data.onEdit({ type: 'setComment', node: n.id, value: v }),
					{ multiline: true }
				);
			} else if (retargetable) {
				// Pick an existing tag, or "Name : TYPE" to declare and wire
				// a new one in the same gesture.
				data.requestInput(
					n.label,
					at,
					(v) => {
						const m = v.match(/^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([A-Za-z_][A-Za-z0-9_]*)$/);
						if (m) data.onEdit({ type: 'retarget', node: n.id, value: m[1], declareType: m[2] });
						else data.onEdit({ type: 'retarget', node: n.id, value: v });
					},
					{ suggest: 'tags' }
				);
			} else if (editableConst) {
				// A constant can be retyped as a literal OR replaced by a tag.
				data.requestInput(n.label, at, (v) => data.onEdit({ type: 'setLiteral', node: n.id, value: v }), {
					suggest: 'tags'
				});
			} else if (n.kind === 'fb' && data.editable) {
				// PLC-IDE split, as in ladder: the HEADER (instance name and
				// type) renames — one edit covering the declaration and every
				// inst.pin read; the body opens the instance inspector (this
				// instance's live data).
				if ((ev.target as HTMLElement).closest?.('.title')) {
					data.requestInput(n.label, at, (v) => data.onEdit({ type: 'rename', node: n.id, value: v }));
				} else {
					data.onInspect?.({ name: n.label, type: n.type ?? '', ins: n.ins, outs: n.outs });
				}
			} else if (renameable) {
				data.requestInput(n.wire ?? '', at, (v) => data.onEdit({ type: 'rename', node: n.id, value: v }));
			}
		};
		el.addEventListener('dblclick', handler);
		return { destroy: () => el.removeEventListener('dblclick', handler) };
	}

	const problems = $derived(data.problems ?? []);
	// Live value pills, mirroring the text editor's inline decorations:
	// variable chips and coils show their value; FB instances show each
	// output pin's value off the streamed instance struct. Literals need no
	// pill (the label IS the value); plain blocks have no runtime identity
	// (their wires are inlined expressions). Hidden in diff mode.
	const chipVal = $derived(
		data.editable && chip && !n.src ? liveValue(n.label) : undefined
	);
	const fbStruct = $derived(data.editable && n.kind === 'fb' ? liveValue(n.label) : undefined);
	// A read of a declared VAR_EXTERNAL that the live controller has no tag
	// for: this READ will fault the scan (writes create tags, reads don't) —
	// warn at design time instead. Coils skip it: a write self-creates.
	const missingTag = $derived(
		data.editable &&
			n.kind === 'input' &&
			!n.src &&
			!n.ghost &&
			data.extNames?.has(n.label.split('.')[0].toLowerCase()) === true &&
			liveMissing(n.label)
	);
	const retargetHint = 'double-click to change the tag ("Name : TYPE" declares a new one)';
	const title = $derived.by(() => {
		if (note) return 'comment — double-click to edit (Ctrl+Enter saves); empty text deletes';
		if (n.ghost) {
			return n.kind === 'coil'
				? `${n.label} — bare output reference: drop a wire on it to write the coil`
				: `${n.label} — bare input reference: drag its pin onto a block input to use it`;
		}
		// Diagnostics take over the tooltip — the same message as the squiggle.
		const missingNote = missingTag
			? `no '${n.label}' tag on the controller — this read faults the scan; seed it, drive it from a driver/HMI, or write it from logic first`
			: '';
		if (problems.length) {
			const msgs = problems.map((p) => p.message).join('\n');
			return [msgs, missingNote, retargetable ? retargetHint : ''].filter(Boolean).join('\n');
		}
		const base = n.kind === 'fb' ? `${n.label} : ${n.type ?? '?'}` : n.label;
		if (missingNote) return `${base} — ${missingNote}\n${retargetHint}`;
		if (retargetable) return `${base} — ${retargetHint}`;
		if (editableConst) return `${base} — double-click to edit`;
		if (n.kind === 'fb' && data.editable)
			return `${base} — double-click the header to rename the instance, the body to inspect it`;
		if (renameable) return `${base} — double-click to rename`;
		return base;
	});
</script>

{#if note}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="note {n.status ?? ''}" style="width: {n.w}px; height: {n.h}px; line-height: {NOTE_LINE_H}px" {title} use:dblEdit>
		{#each n.label.split('\n') as line, i (i)}<div class="noteline">{line}</div>{/each}
	</div>
{:else if chip}
	<div
		class="chip {n.kind} {n.status ?? ''}"
		class:ghost={n.ghost}
		class:editable={editableConst || retargetable}
		class:missing={missingTag}
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
		{#if chipVal !== undefined}
			<span class="nx-pill val below" class:off={!live.fresh} title="{n.label} = {formatLive(chipVal)} (live)">{formatLive(chipVal)}</span>
		{/if}
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
			{#if fbStruct !== undefined && member(fbStruct, pin) !== undefined}
				<span
					class="nx-pill val beside"
					class:off={!live.fresh}
					style="top: {pinOffset(n, pin, 'out') - 8}px"
					title="{n.label}.{pin} = {formatLive(member(fbStruct, pin))} (live)"
				>{formatLive(member(fbStruct, pin))}</span>
			{/if}
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
		--ink: var(--nx-ink);
		box-sizing: border-box;
		background: var(--nx-panel-bg);
		border: 1.2px solid var(--ink);
		color: var(--ink);
		font-family: var(--nx-mono);
		font-size: 12px;
		position: relative;
	}
	.added {
		--ink: var(--nx-added);
	}
	.removed {
		--ink: var(--nx-removed);
		border-style: dashed;
		opacity: 0.85;
	}
	.changed {
		--ink: var(--nx-changed);
	}
	/* A declared external whose tag doesn't exist on the live controller:
	   this read faults the scan at runtime — amber warning now instead.
	   Compiler diagnostics (below) outrank it. */
	.missing {
		border-color: var(--nx-warn) !important;
		box-shadow: 0 0 8px color-mix(in srgb, var(--nx-warn) 40%, transparent);
	}
	/* A diagnostic on this element's line: red border + badge; the tooltip
	   carries the compiler's message, exactly like the squiggle's hover. */
	.problem {
		border-color: var(--nx-err) !important;
		box-shadow: 0 0 8px color-mix(in srgb, var(--nx-err) 45%, transparent);
	}
	.badge {
		position: absolute;
		top: -7px;
		right: -7px;
		width: 14px;
		height: 14px;
		border-radius: 50%;
		background: var(--nx-err);
		color: var(--nx-bg);
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
	/* A ghost is a reference that exists only on the canvas so far — dashed
	   until a wire makes it real netlist text. */
	.chip.ghost {
		border-style: dashed;
		opacity: 0.75;
	}
	.note {
		box-sizing: border-box;
		padding: 4px 9px;
		font-family: var(--nx-mono);
		font-size: 11px;
		font-style: italic;
		color: var(--nx-comment);
		background: color-mix(in srgb, var(--nx-panel-bg) 55%, transparent);
		border: 1px dashed color-mix(in srgb, var(--nx-comment) 55%, transparent);
		border-radius: 4px;
		cursor: pointer;
		white-space: nowrap;
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
	/* Live value pill — color chrome comes from the shared .nx-pill; only
	   node-specific size and placement live here. */
	.val {
		position: absolute;
		font-size: 9px;
		line-height: 1;
		padding: 2px 5px;
		pointer-events: auto;
	}
	.val.below {
		left: 50%;
		top: 100%;
		transform: translate(-50%, 3px);
	}
	.val.beside {
		right: -10px;
		transform: translateX(100%);
	}
	:global(.svelte-flow__handle) {
		width: 7px;
		height: 7px;
		background: var(--nx-blue);
		border: none;
		opacity: 0.65;
	}
	:global(.svelte-flow__handle.plus-handle) {
		background: transparent;
		border: 1.2px dashed var(--nx-blue);
		width: 9px;
		height: 9px;
	}
	.pin.plus {
		color: var(--nx-blue);
		opacity: 0.9;
		font-weight: 700;
	}
	/* Selection must be unmissable, single node included: the wrapper gets
	   an outline + glow, AND the element itself recolors (multiple cues so
	   no theme/zoom combination can hide it). */
	:global(.svelte-flow__node.selected) {
		outline: 2px solid var(--nx-accent);
		outline-offset: 3px;
		border-radius: 4px;
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--nx-accent) 25%, transparent),
			0 0 14px color-mix(in srgb, var(--nx-accent) 45%, transparent);
	}
	:global(.svelte-flow__node.selected) .chip,
	:global(.svelte-flow__node.selected) .block {
		border-color: var(--nx-accent);
		border-width: 2px;
		background: color-mix(in srgb, var(--nx-accent) 14%, var(--nx-panel-bg));
	}
</style>
