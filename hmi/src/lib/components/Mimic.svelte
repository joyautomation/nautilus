<script lang="ts">
	// Renders a MimicDoc live: an SVG underlay of pipe runs, kit components
	// absolutely placed above it, all scaled to the container width. Feed it
	// `frame.tags` and the bindings animate with the process. Pass extra
	// components through `registry` to place your own equipment — the
	// built-ins are an easy button, not a wall.
	import type { Component } from 'svelte';
	import Tank from './Tank.svelte';
	import Pump from './Pump.svelte';
	import Valve from './Valve.svelte';
	import Gauge from './Gauge.svelte';
	import Sparkline from './Sparkline.svelte';
	import Pipe from './Pipe.svelte';
	import { makeGetPort, pointsToPath, resolveBindings, resolveRuntimePorts, routedPoints } from '../mimic.js';
	import type { EquipmentBox, MimicDoc, MimicEquipment, MimicLabel } from '../mimic.js';

	let {
		doc,
		tags = {},
		registry = {},
		onequipmentclick,
		minScale = 0
	}: {
		doc: MimicDoc;
		tags?: Record<string, unknown>;
		/** Extra/override components addressable from equipment[].component. */
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		registry?: Record<string, Component<any>>;
		/** Present = equipment is clickable (open a faceplate, navigate…). */
		onequipmentclick?: (eq: MimicEquipment) => void;
		/**
		 * Floor under which the mimic stops shrinking. When the container is
		 * narrow enough that `containerWidth / doc.canvas.width` would drop
		 * below this, the mimic is drawn at `minScale` instead and the frame
		 * becomes a scrollable viewport onto it, rather than rendering the
		 * plant illegibly small on a phone. `0` (the default) is exactly
		 * today's behaviour: no floor, the mimic always shrinks to fit the
		 * container width.
		 */
		minScale?: number;
	} = $props();

	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const builtin: Record<string, Component<any>> = { Tank, Pump, Valve, Gauge, Sparkline };
	const components = $derived({ ...builtin, ...registry });

	// Scale the logical canvas to the container width (height follows),
	// floored at minScale — below that, the frame scrolls instead of
	// shrinking the canvas further.
	let host = $state<HTMLDivElement | null>(null);
	let frameWidth = $state(0);
	$effect(() => {
		if (!host) return;
		const el = host;
		const update = () => (frameWidth = el.clientWidth);
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});
	const rawScale = $derived(frameWidth ? frameWidth / doc.canvas.width : 1);
	const floored = $derived(minScale > 0 && frameWidth > 0 && rawScale < minScale);
	const scale = $derived(floored ? minScale : rawScale);

	// A bound label's live value (MimicLabel.bind), resolved through the same
	// resolveBindings() path equipment bindings use — one mechanism, "!"
	// negation and all — and formatted for the readout. '—' until the tag
	// resolves to a finite number, same as Trend/ScanDiagnostics.
	const labelValue = (l: MimicLabel): string => {
		const v = resolveBindings({ value: l.bind! }, tags).value;
		return typeof v === 'number' && isFinite(v) ? v.toFixed(l.decimals ?? 1) : '—';
	};

	const eqProps = (eq: MimicEquipment): Record<string, unknown> => ({
		...(eq.width !== undefined ? { width: eq.width } : {}),
		...(eq.label !== undefined ? { label: eq.label } : {}),
		...(eq.props ?? {}),
		...resolveBindings(eq.bind, tags)
	});

	// ── pipe end anchors (MimicPipe.from/to) ─────────────────────────────────
	// Anchored pipe ends resolve against an equipment's ACTUAL rendered box —
	// same DOM-measured approach as the mimic editor's EditorCanvas.svelte
	// (eqBox()/eqEls) so an anchor lands identically here and there. Equipment
	// carries no intrinsic size of its own (only an optional `width`), so
	// there's no way to compute this from the doc alone.
	const eqEls = new Map<string, HTMLElement>();
	/** Bumped by each equipment's own ResizeObserver — read (not written) by
	 * pipePaths below purely to force it to recompute after layout settles
	 * (first paint, a component's size changing with its bound props, …). */
	let boxVersion = $state(0);
	function regEl(node: HTMLElement, id: string) {
		eqEls.set(id, node);
		const ro = new ResizeObserver(() => boxVersion++);
		ro.observe(node);
		return {
			update(next: string) {
				eqEls.delete(id);
				id = next;
				eqEls.set(id, node);
			},
			destroy() {
				eqEls.delete(id);
				ro.disconnect();
			}
		};
	}
	function eqBox(eq: MimicEquipment): EquipmentBox {
		const el = eqEls.get(eq.id);
		return { x: eq.x, y: eq.y, w: el?.offsetWidth || eq.width || 100, h: el?.offsetHeight || 80 };
	}
	function findEq(id: string): MimicEquipment | undefined {
		return (doc.equipment ?? []).find((e) => e.id === id);
	}
	const getPort = makeGetPort(
		(id) => {
			const eq = findEq(id);
			return eq ? eqBox(eq) : undefined;
		},
		(id) => {
			const eq = findEq(id);
			return eq ? resolveRuntimePorts(eq) : undefined;
		}
	);
	/** One routed path per pipe — recomputed whenever the doc changes OR any
	 * equipment's measured box changes (moving/resizing an anchored end's
	 * equipment must move the pipe with it, with no explicit wiring beyond
	 * this dependency: routedPoints() re-derives the anchor's position from
	 * the fresh box on every call). */
	const pipePaths = $derived.by(() => {
		void boxVersion;
		return (doc.pipes ?? []).map((p) => ({ pipe: p, d: pointsToPath(routedPoints(p, getPort)) }));
	});

	/** Paint order for the pipe network — see the junction note in Pipe.svelte. */
	const PIPE_LAYERS = ['wall', 'bore', 'flow'] as const;
</script>

<div class="mimic" class:scrolling={floored} bind:this={host} style="height: {doc.canvas.height * scale}px">
	<div
		class="scroll-host"
		style:width={floored ? `${doc.canvas.width * scale}px` : undefined}
		style:height={floored ? `${doc.canvas.height * scale}px` : undefined}
	>
		<div
			class="canvas"
			style="width: {doc.canvas.width}px; height: {doc.canvas.height}px; transform: scale({scale})"
		>
			<svg
				class="pipes"
				width={doc.canvas.width}
				height={doc.canvas.height}
				viewBox="0 0 {doc.canvas.width} {doc.canvas.height}"
				aria-hidden="true"
			>
				<!-- Layered, not per-pipe: every wall, then every bore, then
				     every flow overlay, so runs UNION at junctions — a branch's
				     bore merges into the main's instead of the later pipe's
				     background-coloured bore punching a slot across the earlier
				     pipe's wall. See the junction note in Pipe.svelte. -->
				{#each PIPE_LAYERS as layer (layer)}
					{#each pipePaths as { pipe: p, d } (p.id)}
						<Pipe
							{d}
							{...(p.color !== undefined ? { color: p.color } : {})}
							{...(p.props ?? {})}
							{...resolveBindings(p.bind, tags)}
							{layer}
						/>
					{/each}
				{/each}
			</svg>

			{#each doc.equipment ?? [] as eq (eq.id)}
				{@const C = components[eq.component]}
				{#if onequipmentclick}
					<button
						type="button"
						class="eq clickable"
						style="left: {eq.x}px; top: {eq.y}px"
						aria-label={eq.label ?? eq.id}
						use:regEl={eq.id}
						onclick={() => onequipmentclick(eq)}
					>
						{#if C}<C {...eqProps(eq)} />{:else}<span class="unknown">{eq.component}?</span>{/if}
					</button>
				{:else}
					<div class="eq" style="left: {eq.x}px; top: {eq.y}px" use:regEl={eq.id}>
						{#if C}<C {...eqProps(eq)} />{:else}<span class="unknown">{eq.component}?</span>{/if}
					</div>
				{/if}
			{/each}

			{#each doc.labels ?? [] as l, i (i)}
				{#if l.bind}
					<span class="lbl readout" style="left: {l.x}px; top: {l.y}px"
						>{#if l.text}{l.text}{/if}<b class="num">{labelValue(l)}</b
						>{#if l.unit}<span>{l.unit}</span>{/if}</span
					>
				{:else}
					<span class="lbl" style="left: {l.x}px; top: {l.y}px">{l.text}</span>
				{/if}
			{/each}
		</div>
	</div>
</div>

<style>
	.mimic {
		position: relative;
		/* Geometry is never clamped to the canvas rect (see EditorCanvas.svelte
		   for the editor-side twin of this comment) — a doc can legitimately
		   have pipe points or equipment sitting past the edge mid-edit.
		   Clipping is the render-side backstop so a shipped HMI never shows a
		   stray fragment outside its frame. inset(-4px) leaves a hair of
		   room so a round pipe cap landing exactly on the boundary isn't cut
		   in half — anything further out still gets clipped. */
		clip-path: inset(-4px);
	}
	/* Only entered when `minScale` has floored the render — see the prop's
	   doc comment above. Lets the frame scroll to the real (floored) size
	   instead of clipping it. */
	.mimic.scrolling {
		overflow: auto;
		touch-action: pan-x pan-y pinch-zoom;
		-webkit-overflow-scrolling: touch;
	}
	/* A plain, unsized wrapper except in the floored state, where its
	   explicit width/height (set inline, above) give the frame a real
	   scrollable content box — rather than relying on the UA to fold the
	   absolutely-positioned, transform-scaled `.canvas` below into the
	   frame's scrollable overflow, which is inconsistent across browsers
	   (notably iOS Safari). */
	.scroll-host {
		display: block;
	}
	.canvas {
		position: absolute;
		top: 0;
		left: 0;
		transform-origin: top left;
	}
	.pipes {
		position: absolute;
		inset: 0;
		overflow: hidden;
	}
	.eq {
		position: absolute;
		margin: 0;
		padding: 0;
		border: none;
		background: none;
		color: inherit;
		font: inherit;
		text-align: inherit;
	}
	.eq.clickable {
		cursor: pointer;
		border-radius: 8px;
	}
	.eq.clickable:hover {
		outline: 2px solid var(--s1, #3987e5);
		outline-offset: 2px;
	}
	.eq.clickable:focus-visible {
		outline: 2px solid var(--s1, #3987e5);
		outline-offset: 2px;
	}
	.unknown {
		display: inline-block;
		padding: 4px 8px;
		border: 1px dashed var(--crit, #e5484d);
		border-radius: 6px;
		color: var(--crit, #e5484d);
		font-size: var(--font-2xs);
		font-family: var(--mono, monospace);
	}
	.lbl {
		position: absolute;
		font-size: var(--font-2xs);
		color: var(--muted, #8b949e);
		white-space: nowrap;
	}
	/* A bound label reads as a readout chip: the static text stays muted like
	   any label, the live value steps up to ink on a --surface-2 pill. */
	.lbl.readout {
		display: inline-flex;
		align-items: baseline;
		gap: 0.35em;
		padding: 2px 7px;
		background: var(--surface-2, #232321);
		border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		border-radius: 6px;
	}
	.lbl.readout .num {
		color: var(--ink, #e6e6e6);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}
</style>
