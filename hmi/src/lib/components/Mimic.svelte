<script lang="ts">
	// Renders a MimicDoc live: an SVG underlay of pipe runs, kit components
	// absolutely placed above it, all scaled to the container width (or, with
	// fit="contain", to the room it has, both ways). Feed it
	// `frame.tags` and the bindings animate with the process. Pass extra
	// components through `registry` to place your own equipment — the
	// built-ins are an easy button, not a wall.
	import type { Component } from 'svelte';
	import Tank from './Tank.svelte';
	import Heater from './Heater.svelte';
	import Pump from './Pump.svelte';
	import Valve from './Valve.svelte';
	import Gauge from './Gauge.svelte';
	import Sparkline from './Sparkline.svelte';
	import ReadoutRow from './ReadoutRow.svelte';
	import CommandButton from './CommandButton.svelte';
	import WriteToggle from './WriteToggle.svelte';
	import OffPage from './OffPage.svelte';
	import SetpointGroup from './SetpointGroup.svelte';
	import Pipe from './Pipe.svelte';
	import StatusPill from './StatusPill.svelte';
	import { sampleTag, tagHistory } from '../taghistory.svelte.js';
	import { equipmentRefs, labelRefs, BUILTIN_PORTS, autoRoutes, brokenPipes, expandUnits, labelState, orthogonalPoints, makeGetPort, partRect, pipeCrossings, pipeTees, CROSSED_MANY, type PipeCrossing, pointsToPath, resolveBindings, resolveRuntimePorts, routedPoints, unresolvedBindings, unresolvedMessage } from '../mimic.js';
	import { STATUS_META, worstStatus, type ValueStatus } from '../quality.js';
	import type { Quality } from '../types.js';
	import type { EquipmentBox, MimicDoc, MimicEquipment, MimicLabel, MimicLook } from '../mimic.js';

	let {
		doc: source,
		tags = {},
		quality,
		registry = {},
		onequipmentclick,
		clickable,
		write,
		selected = null,
		trends = false,
		minScale = 0,
		fit = 'width',
		look
	}: {
		doc: MimicDoc;
		/** Override the doc's `look` (how the equipment is drawn). */
		look?: MimicLook;
		tags?: Record<string, unknown>;
		/**
		 * The frame's per-tag quality (`frame.quality`: only the non-good
		 * tags). A label or equipment reading a tag that is BAD (or not
		 * connected) or STALE says so, with a badge, never a confident value
		 * (#1056). A tag with no entry draws as it always did. Omit on a
		 * controller that reports no quality.
		 */
		quality?: Record<string, Quality>;
		/** Extra/override components addressable from equipment[].component. */
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		registry?: Record<string, Component<any>>;
		/** Present = equipment is clickable (open a faceplate, navigate…). */
		onequipmentclick?: (eq: MimicEquipment) => void;
		/** Which equipment a click goes to (with `onequipmentclick`): the
		 *  rest draw as they would with no click at all. Default: all. */
		clickable?: (eq: MimicEquipment) => boolean;
		/** A sparkline of the last few minutes in every bound label whose
		 *  value is a number, unless the label says `trend: false` (a label
		 *  that says `trend: true` gets one either way). */
		trends?: boolean;
		/**
		 * Writes one tag on the controller (`RealtimeClient.writeTag` is one):
		 * resolves to null when it landed, else the refusal reason. Present =
		 * the screen is operable: every equipment gets it as its `write` prop,
		 * so the kit's CommandButton / WriteToggle and a project component's
		 * own controls can command the plant. Absent, they show the reason.
		 */
		write?: (name: string, value: unknown) => Promise<string | null>;
		/** The id of the equipment to mark as selected (its faceplate open,
		 *  say): it keeps a soft glow, as hover gives one for a moment. */
		selected?: string | null;
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
		/**
		 * How the drawing meets its container. `'width'` (the default): scaled
		 * to the container's width, as tall as that makes it, so a tall
		 * drawing runs on below and the page scrolls. `'contain'`: the whole
		 * drawing at once, scaled to fit the container's width and the height
		 * from the mimic's top to the container's bottom, and centred across.
		 * For an overview that must show everything without scrolling (naut's
		 * own screens use it). The container needs a height of its own (a
		 * `flex: 1` main in a full-height shell); in one that grows with its
		 * content this is the same as `'width'`.
		 */
		fit?: 'width' | 'contain';
	} = $props();
	/** The document with its units drawn out (expandUnits): everything below
	 * works on this, so a repeated filter cell is one more piece of equipment. */
	const doc = $derived(expandUnits(source));

	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const builtin: Record<string, Component<any>> = { Tank, Heater, Pump, Valve, Gauge, Sparkline, CommandButton, WriteToggle, OffPage, SetpointGroup };
	const components = $derived({ ...builtin, ...registry });

	// Scale the logical canvas to the container width (height follows),
	// floored at minScale — below that, the frame scrolls instead of
	// shrinking the canvas further.
	let host = $state<HTMLDivElement | null>(null);
	let frameWidth = $state(0);
	/** fit="contain": the height from the mimic's top to the bottom of its
	 * container's content box, measured with the mimic itself folded to no
	 * height for the moment of the reading: a container with a height of its
	 * own gives its room, one that grows with its content gives none (and
	 * the width rules). 0 = not measured, or no room. */
	let roomHeight = $state(0);
	$effect(() => {
		if (!host) return;
		const el = host;
		const box = el.parentElement;
		const contain = fit === 'contain';
		const update = () => {
			frameWidth = el.clientWidth;
			if (!contain || !box) return;
			const was = el.style.height;
			const scrolled = box.scrollTop;
			el.style.height = '0px';
			const pr = box.getBoundingClientRect();
			const pb = parseFloat(getComputedStyle(box).paddingBottom) || 0;
			const bottom = pr.top + box.clientTop + box.clientHeight - pb;
			const room = Math.floor(bottom - el.getBoundingClientRect().top);
			el.style.height = was;
			box.scrollTop = scrolled;
			// less than a strip is no room at all: the container grows with us
			roomHeight = room >= 48 ? room : 0;
		};
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		if (contain && box) ro.observe(box);
		return () => ro.disconnect();
	});
	const widthScale = $derived(frameWidth ? frameWidth / doc.canvas.width : 1);
	const rawScale = $derived(
		fit === 'contain' && roomHeight > 0 ? Math.min(widthScale, roomHeight / doc.canvas.height) : widthScale
	);
	const floored = $derived(minScale > 0 && frameWidth > 0 && rawScale < minScale);
	const scale = $derived(floored ? minScale : rawScale);
	/** Left offset that centres a drawing narrower than its frame (contain). */
	const inset = $derived(floored || !frameWidth ? 0 : Math.max(0, (frameWidth - doc.canvas.width * rawScale) / 2));

	// A bound label's live value (MimicLabel.bind), resolved through the same
	// resolveBindings() path equipment bindings use — one mechanism, "!"
	// negation and all — and formatted for the readout. '—' until the tag
	// resolves to a finite number, same as Trend/ScanDiagnostics. A STRING
	// tag (a sequence's step) shows as it is.
	const labelValue = (l: MimicLabel): string => {
		const v = resolveBindings({ value: l.bind! }, tags).value;
		const word = l.values && (typeof v === 'string' || typeof v === 'number') ? l.values[String(v)] : undefined;
		if (word !== undefined) return word;
		if (typeof v === 'string') return v || '—';
		return typeof v === 'number' && isFinite(v) ? v.toFixed(l.decimals ?? 1) : '—';
	};
	const labelRaw = (l: MimicLabel): unknown => resolveBindings({ value: l.bind! }, tags).value;
	/** The bound labels that draw a sparkline: each keeps the last few
	 * minutes of its tag (taghistory), sampled once a second. */
	const trended = (l: MimicLabel): boolean => !!l.bind && !l.values && (l.trend ?? trends) && typeof labelRaw(l) === 'number';
	$effect(() => {
		const id = setInterval(() => {
			for (const l of doc.labels ?? []) if (l.bind && (l.trend ?? trends)) sampleTag(l.bind, labelRaw(l));
		}, 1000);
		return () => clearInterval(id);
	});

	/** Each label's sense line (MimicLabel.at): a right-angled run from
	 * just inside the label to its point, which gets a dot. */
	const senses = $derived(
		(doc.labels ?? []).flatMap((l) =>
			Array.isArray(l.at) && l.at.length === 2
				? [{ d: pointsToPath(orthogonalPoints([[l.x + 10, l.y + 10], l.at])), x: l.at[0], y: l.at[1] }]
				: []
		)
	);

	const drawLook = $derived(look ?? doc.look ?? 'isa101');
	/** A project component (one passed in `registry`, not a kit built-in) gets
	 * a box of the document's width and height, so a drawing sized to 100%
	 * fills it; the built-ins size themselves from those props. */
	const customBox = (eq: MimicEquipment): string =>
		eq.component in BUILTIN_PORTS
			? ''
			: (eq.width ? `; width: ${eq.width}px` : '') + (eq.height ? `; height: ${eq.height}px` : '');
	const eqProps = (eq: MimicEquipment): Record<string, unknown> => ({
		look: drawLook,
		...(eq.width !== undefined ? { width: eq.width } : {}),
		...(eq.height !== undefined ? { height: eq.height } : {}),
		...(eq.label !== undefined ? { label: eq.label } : {}),
		...(eq.props ?? {}),
		...resolveBindings(eq.bind, tags),
		// a SetpointGroup reads the tags its items name
		...(eq.component === 'SetpointGroup' || (eq.component === 'Tank' && eq.props?.marks) ? { tags } : {}),
		write: write ?? readOnly
	});
	/** How a label / equipment is qualified: the worst of the tags it reads. */
	const labelQ = (l: MimicLabel): ValueStatus => worstStatus(quality, labelRefs(l));
	const eqQ = (eq: MimicEquipment): ValueStatus => worstStatus(quality, equipmentRefs(eq));
	const qWord = (st: ValueStatus): string => STATUS_META[st].label.toUpperCase();
	const flagged = (st: ValueStatus): boolean => st === 'bad' || st === 'stale' || st === 'substituted';
	const readOnly = async (): Promise<string | null> => 'This screen is read-only';

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

	// Equipment whose glow is fading out: it keeps its filter for the
	// fade, then goes back to none. (A filter on the scaled canvas renders
	// its element as an image at the canvas's own size, which the scale
	// then blurs, so nothing keeps one while it is not glowing.)
	const FADE_MS = 200;
	let fading = $state<Record<string, true>>({});
	function fadeOut(id: string) {
		fading = { ...fading, [id]: true };
		setTimeout(() => {
			const { [id]: _, ...rest } = fading;
			fading = rest;
		}, FADE_MS);
	}
	// A selection that moves on fades out like a hover that ends. Before
	// the DOM updates, so .fading arrives with .selected's removal: a
	// frame between them would drop the glow to nothing at once.
	let wasSelected: string | null = null;
	$effect.pre(() => {
		if (wasSelected && wasSelected !== selected) fadeOut(wasSelected);
		wasSelected = selected;
	});
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
		},
		(id, part) => partRect(eqEls.get(id), part)
	);
	/** One routed path per pipe — recomputed whenever the doc changes OR any
	 * equipment's measured box changes (moving/resizing an anchored end's
	 * equipment must move the pipe with it, with no explicit wiring beyond
	 * this dependency: routedPoints() re-derives the anchor's position from
	 * the fresh box on every call). */
	// Labels, measured: a routed pipe keeps off them.
	const labelEls: (HTMLElement | undefined)[] = [];
	function regLabel(node: HTMLElement, i: number) {
		labelEls[i] = node;
		const ro = new ResizeObserver(() => boxVersion++);
		ro.observe(node);
		return {
			destroy() {
				if (labelEls[i] === node) labelEls[i] = undefined;
				ro.disconnect();
			}
		};
	}
	/** The `routing: 'auto'` pipes, routed round the equipment and labels
	 * as measured (autoRoutes; route.ts). */
	const routes = $derived.by(() => {
		void boxVersion;
		// placed from the doc (fresh as it moves), sized as measured: a label
		// sits 3px left of its x (.lbl's margin) but for a row, a bound or
		// state label drawn as bare text, and a centred one, whose x is its
		// middle
		const labels = (doc.labels ?? []).flatMap((l, i) => {
			const el = labelEls[i];
			if (!el) return [];
			const w = el.offsetWidth;
			const flush = l.as === 'row' || (l.as === 'text' && (!!l.bind || !!l.state));
			const x = l.align === 'center' ? l.x - w / 2 : flush ? l.x : l.x - 3;
			return [{ x, y: l.y, w, h: el.offsetHeight }];
		});
		return autoRoutes(doc, getPort, (id) => {
			const eq = findEq(id);
			return eq ? eqBox(eq) : undefined;
		}, labels);
	});
	const pipePaths = $derived.by(() => {
		void boxVersion;
		return (doc.pipes ?? []).map((p) => {
			const pts = p.routing === 'auto' ? (routes.paths.get(p.id) ?? []) : routedPoints(p, getPort);
			return { pipe: p, pts, d: pts.length > 1 ? pointsToPath(pts) : '' };
		});
	});
	/** Routed pipes with no clear way, for naut check. */
	const unrouted = $derived(routes.failed.length > 0 ? JSON.stringify(routes.failed) : undefined);

	/** Where pipes cross without joining. Drawn as on a P&ID: the pipe
	 * passing over (the flatter one, a header) stays whole and the other is
	 * broken either side of it, so a crossing never reads as the tee the
	 * layered walls would otherwise make of it. The under pipe is masked out
	 * of a small square on each of its crossings: the over pipe's walls,
	 * which its bore would have cut, show whole, with a clear gap either
	 * side. */
	const crossings = $derived(pipeCrossings(pipePaths.map((p) => p.pts)));
	/** Where a branch joins a run: a dot, as on a schematic (pipeTees). */
	const tees = $derived(pipeTees(pipePaths.map((p) => p.pts)));
	const uid = $props.id();
	/** The crossings each pipe passes under, by its index: its mask. */
	const unders = $derived.by(() => {
		const m = new Map<number, PipeCrossing[]>();
		for (const c of crossings) m.set(c.under, [...(m.get(c.under) ?? []), c]);
		return m;
	});
	/** Pipes that pass under three or more others, most first, as JSON on
	 * the root for `naut check`'s render: a drop crossing a stack of
	 * headers to reach its own can't be traced by eye. Absent when none. */
	const crossed = $derived.by(() => {
		const many = [...unders]
			.filter(([, cs]) => cs.length >= CROSSED_MANY)
			.map(([i, cs]) => ({ id: pipePaths[i].pipe.id, n: cs.length }))
			.sort((a, b) => b.n - a.n);
		return many.length > 0 ? JSON.stringify(many) : undefined;
	});
	/** Half the side of a crossing's square: half the over pipe's 10 px
	 * wall plus a 6 px gap the under pipe stops short by, so a crossing
	 * reads as one at the size an operator sees it. */
	const XING = 11;

	/** Pipes that draw nothing (an end that isn't there, or no length), as
	 * JSON on the root for `naut check`'s render; absent when there are none. */
	const broken = $derived.by(() => {
		void boxVersion;
		const b = brokenPipes(doc.pipes ?? [], getPort);
		return b.length > 0 ? JSON.stringify(b) : undefined;
	});

	/** Bindings that name no tag in the live frame: shown, never silent. */
	const unresolved = $derived(unresolvedBindings(doc, tags));

	/** Paint order for the pipe network — see the junction note in Pipe.svelte. */
	const PIPE_LAYERS = ['wall', 'bore', 'flow'] as const;
</script>

<div
	class="mimic"
	class:scrolling={floored}
	bind:this={host}
	data-broken-pipes={broken}
	data-unresolved={unresolved.length > 0 ? JSON.stringify(unresolved) : undefined}
	data-crossed={crossed}
	data-unrouted={unrouted}
	data-scale={scale.toFixed(3)}
	data-canvas="{doc.canvas.width}x{doc.canvas.height}"
	style="height: {doc.canvas.height * scale}px"
>
	{#if unresolved.length > 0}
		<div class="unresolved" role="status">
			{unresolvedMessage(unresolved)}
		</div>
	{/if}
	<div
		class="scroll-host"
		style:width={floored ? `${doc.canvas.width * scale}px` : undefined}
		style:height={floored ? `${doc.canvas.height * scale}px` : undefined}
	>
		<div
			class="canvas"
			style="width: {doc.canvas.width}px; height: {doc.canvas.height}px; left: {inset}px; transform: scale({scale})"
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
				{#if unders.size > 0}
					<defs>
						{#each [...unders] as [p, cs] (p)}
							<mask id="{uid}-u{p}" maskUnits="userSpaceOnUse" x="0" y="0" width={doc.canvas.width} height={doc.canvas.height}>
								<rect x="0" y="0" width={doc.canvas.width} height={doc.canvas.height} fill="white" />
								{#each cs as c, i (i)}
									<rect x={c.x - XING} y={c.y - XING} width={XING * 2} height={XING * 2} fill="black" />
								{/each}
							</mask>
						{/each}
					</defs>
				{/if}
				{#each PIPE_LAYERS as layer (layer)}
					{#each pipePaths as { pipe: p, d }, i (p.id)}
						<g mask={unders.has(i) ? `url(#${uid}-u${i})` : undefined} data-pipe={layer === 'wall' ? p.id : undefined}>
							<Pipe
								{d}
								{...(p.color !== undefined ? { color: p.color } : {})}
								{...(p.props ?? {})}
								{...resolveBindings(p.bind, tags)}
								{layer}
								look={drawLook}
							/>
						</g>
					{/each}
				{/each}
				{#each tees as t, i (i)}
					<circle class="tee" cx={t.x} cy={t.y} r={drawLook === 'isa101' ? 5 : 7} fill="var(--pipe-wall, var(--edge, #7a7973))" />
				{/each}
				<!-- sense lines: a label joined to the point it reads; the label,
				     drawn over this layer, hides the line's end under it -->
				{#each senses as s, i (i)}
					<g class="sense">
						<path d={s.d} fill="none" stroke="var(--muted, #898781)" stroke-width="1.5" stroke-dasharray="5 4" />
						<circle cx={s.x} cy={s.y} r="4" fill="var(--muted, #898781)" />
					</g>
				{/each}
			</svg>

			{#each doc.equipment ?? [] as eq (eq.id)}
				{@const C = components[eq.component]}
				{#if onequipmentclick && (!clickable || clickable(eq))}
					<button
						type="button"
						class="eq clickable"
						class:qflag={flagged(eqQ(eq))}
						class:behind={eq.behind}
						class:selected={eq.id === selected}
						class:fading={fading[eq.id]}
						aria-pressed={eq.id === selected}
						onpointerleave={() => fadeOut(eq.id)}
						onblur={() => fadeOut(eq.id)}
						style="left: {eq.x}px; top: {eq.y}px{customBox(eq)}"
						aria-label={eq.label ?? eq.id}
						use:regEl={eq.id}
						onclick={() => onequipmentclick(eq)}
					>
						{#if C}<C {...eqProps(eq)} />{:else}<span class="unknown">{eq.component}?</span>{/if}
						{#if flagged(eqQ(eq))}<span class="qbadge eqbadge {eqQ(eq)}" title={STATUS_META[eqQ(eq)].description}>{qWord(eqQ(eq))}</span>{/if}
					</button>
				{:else}
					<div class="eq" class:qflag={flagged(eqQ(eq))} class:behind={eq.behind} class:passive={!!onequipmentclick} style="left: {eq.x}px; top: {eq.y}px{customBox(eq)}" use:regEl={eq.id}>
						{#if C}<C {...eqProps(eq)} />{:else}<span class="unknown">{eq.component}?</span>{/if}
						{#if flagged(eqQ(eq))}<span class="qbadge eqbadge {eqQ(eq)}" title={STATUS_META[eqQ(eq)].description}>{qWord(eqQ(eq))}</span>{/if}
					</div>
				{/if}
			{/each}

			{#each doc.labels ?? [] as l, i (i)}
				{@const st = l.state ? labelState(l, tags) : null}
				{@const lq = labelQ(l)}
				{@const box = `left: ${l.x}px; top: ${l.y}px${l.width ? `; width: ${l.width}px` : ''}`}
				{#if l.as === 'row'}
					<span class="lbl rowlbl" class:qbad={lq === 'bad'} class:qstale={lq === 'stale' || lq === 'substituted'} class:center={l.align === 'center'} style="left: {l.x}px; top: {l.y}px" use:regLabel={i}
						><ReadoutRow
							name={l.text}
							value={!l.bind ? undefined : !l.values && typeof labelRaw(l) === 'number' ? (labelRaw(l) as number) : labelValue(l)}
							unit={l.unit}
							decimals={l.decimals ?? 1}
							history={l.bind && trended(l) ? tagHistory(l.bind) : undefined}
							min={l.min}
							max={l.max}
							state={st}
							width={l.width}
						/>{#if flagged(lq)}<span class="qbadge {lq}" title={STATUS_META[lq].description}>{qWord(lq)}</span>{/if}</span
					>
				{:else if l.bind}
					<span class="lbl readout" class:qbad={lq === 'bad'} class:qstale={lq === 'stale' || lq === 'substituted'} class:plain={l.as === 'text'} class:center={l.align === 'center'} style={box} use:regLabel={i}
						>{#if l.text}{l.text}{/if}<b class="num">{labelValue(l)}</b
						>{#if l.unit}<span>{l.unit}</span>{/if}{#if trended(l)}<span class="spark" aria-hidden="true"
								><Sparkline values={tagHistory(l.bind!)} height={14} yMin={l.min} yMax={l.max} color="var(--ink-2, #c3c2b7)" compact /></span
							>{/if}{#if flagged(lq)}<span class="qbadge {lq}" title={STATUS_META[lq].description}>{qWord(lq)}</span>{/if}</span
					>
				{:else if l.state}
					<span class="lbl state" class:qbad={lq === 'bad'} class:qstale={lq === 'stale' || lq === 'substituted'} class:plain={l.as === 'text'} class:center={l.align === 'center'} style={box} use:regLabel={i}
						>{#if l.text}<span class="name">{l.text}</span>{/if}{#if flagged(lq)}<span class="qbadge {lq}" title={STATUS_META[lq].description}>{qWord(lq)}</span>{:else if st}{#if l.as === 'text'}<b class="word {st.kind ?? 'off'}">{st.text}</b>{:else}<StatusPill kind={st.kind ?? 'off'} label={st.text} />{/if}{/if}</span
					>
				{:else}
					<span class="lbl" class:center={l.align === 'center'} style={box} use:regLabel={i}>{l.text}</span>
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
		/* a `behind` equipment under it must still take its clicks */
		pointer-events: none;
	}
	/* Under the pipe layer: the canvas's transform makes it the stacking
	   context, so -1 paints above the canvas's own background and below
	   every other layer. */
	.eq.behind {
		z-index: -1;
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
	/* A bare <svg> root is inline by default, reserving a few px of
	   descender space below it inside `.eq` — so eqBox()'s offsetHeight came
	   out taller than the drawing, and every anchored pipe end landed a
	   little low (a jog into a pump's inlet, a tilt out of a valve). The
	   mimic editor's EditorCanvas.svelte has had this same rule; with it the
	   runtime measures the box the editor does, and anchors agree. */
	.eq :global(svg) {
		display: block;
	}
	/* Clickable equipment glows rather than boxing itself: a drop shadow
	   follows the drawing's own shape, where an outline traced its whole
	   box (often much larger than the drawing). Hover brightens it and
	   fades the glow in; the selected one and the one keyboard focus is on
	   keep a softer one, enough to see where Tab has landed without
	   lighting up a large drawing. */
	/* The glow's strength, 0 to 1: a registered property, so it animates
	   on its own and the filter built from it follows. At rest there is no
	   filter at all; while glowing, or fading (Mimic adds .fading for
	   FADE_MS), the filter is built from --glow-a, whose colour stays the
	   glow's colour at every step (blue, the kit's highlight and focus
	   colour; --select-glow sets it), so the fade never passes through a dark
	   halo. */
	@property --glow-a {
		syntax: '<number>';
		inherits: false;
		initial-value: 0;
	}
	/* On a screen where some equipment opens a panel, the rest let a click
	   through to whatever is under them (an agitator drawn over its tank),
	   all but their own controls. */
	.eq.passive {
		pointer-events: none;
	}
	.eq.passive :global(:is(button, input, select, textarea, a, label, [role='button'], [tabindex])) {
		pointer-events: auto;
	}
	.eq.clickable {
		cursor: pointer;
		border-radius: 8px;
	}
	.eq.clickable:is(:hover, :focus-visible, .selected, .fading) {
		outline: none;
		filter: brightness(calc(1 + 0.12 * var(--glow-a)))
			drop-shadow(0 0 calc(4px * var(--glow-a)) rgb(from var(--select-glow, var(--s1, #3987e5)) r g b / calc(0.7 * var(--glow-a))));
		transition: --glow-a 0.18s ease;
	}
	.eq.clickable:is(.selected, :focus-visible) {
		--glow-a: 0.7;
	}
	.eq.clickable:hover {
		--glow-a: 1;
	}
	@media (prefers-reduced-motion: reduce) {
		.eq.clickable:is(:hover, :focus-visible, .selected, .fading) {
			transition: none;
		}
	}
	.unresolved {
		position: absolute;
		top: 0;
		left: 0;
		right: 0;
		z-index: 5;
		padding: 2px 8px;
		font-size: 12px;
		background: var(--warn-bg, #fff3cd);
		color: var(--warn-fg, #664d03);
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
	/* A label set on a line breaks the line, as a line label does on a
	   P&ID: a patch of the bore colour behind the text keeps a pipe from
	   running through the letters, and is invisible anywhere else. */
	/* #1056: a reading the controller cannot vouch for. The value stays (an
	   operator needs what it last was) but loses its confidence: dimmed, and a
	   word beside it (colour is never the only cue). No CSS filter: the canvas
	   is scaled and a filter would rasterise it. */
	.qbadge {
		display: inline-block;
		margin-left: 6px;
		padding: 0 5px;
		border: 1px solid var(--q);
		border-radius: 3px;
		font-size: 10px;
		font-weight: 700;
		letter-spacing: 0.05em;
		line-height: 1.5;
		color: var(--q);
		background: var(--surface, transparent);
		white-space: nowrap;
		vertical-align: middle;
	}
	.qbadge.bad { --q: var(--q-bad, var(--crit)); }
	.qbadge.stale { --q: var(--q-stale, var(--warn)); }
	.qbadge.substituted { --q: var(--q-substituted, var(--warn)); }
	.lbl.qbad .num,
	.lbl.qbad :global(.nx-row .val) { color: var(--q-bad, var(--crit)); opacity: 0.6; }
	.lbl.qstale .num,
	.lbl.qstale :global(.nx-row .val) { color: var(--q-stale, var(--warn)); }
	.eq.qflag { outline: 1px dashed var(--muted, #898781); outline-offset: 2px; }
	.eqbadge { position: absolute; top: -9px; right: -4px; margin: 0; z-index: 2; }
	.lbl {
		position: absolute;
		font-size: var(--font-2xs);
		color: var(--muted, #8b949e);
		white-space: nowrap;
		background: var(--pipe-bore, var(--bg, transparent));
		padding: 0 3px;
		margin-left: -3px;
		border-radius: 3px;
	}
	/* A bound label reads as a readout chip: its tag in --ink-2 (plain and
	   well past AA on the chip), the live value in ink on a --surface-2 pill. */
	.lbl.readout {
		color: var(--ink-2, #c3c2b7);
		display: inline-flex;
		align-items: baseline;
		gap: 0.35em;
		padding: 2px 7px;
		background: var(--surface-2, #232321);
		border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		border-radius: 6px;
	}
	.lbl.readout .spark {
		display: block;
		align-self: center;
		width: 40px;
		margin-left: 0.2em;
	}
	/* A state label: its name in --ink-2, then the state as a status chip. */
	.lbl.state {
		color: var(--ink-2, #c3c2b7);
		display: inline-flex;
		align-items: center;
		gap: 0.45em;
		padding: 1px 3px;
		/* the chip has its own ground: no patch cut out of a tank's liquid */
		background: none;
	}
	.lbl.state .name {
		background: var(--pipe-bore, var(--bg, transparent));
		padding: 0 3px;
		border-radius: 3px;
	}
	/* as: text: bare text with a halo of the background colour round
	   its letters (the stroke painted under the fill), legible over a
	   liquid, a pipe or a hatch with no patch cut out of the drawing. */
	.lbl.plain,
	.lbl.plain .name {
		background: none;
		border: 0;
		padding: 0;
		margin-left: 0;
		-webkit-text-stroke: 3px var(--bg, #0d0d0d);
		paint-order: stroke fill;
		stroke-linejoin: round;
	}
	.lbl.plain {
		font-size: var(--font-xs);
	}
	.lbl.plain .word {
		color: var(--ink, #e6e6e6);
		font-weight: 650;
	}
	.lbl.plain .word.good {
		color: var(--good, #0ca30c);
	}
	.lbl.plain .word.warning {
		color: var(--warn, #f5a524);
	}
	.lbl.plain .word.serious {
		color: var(--serious, #f76808);
	}
	.lbl.plain .word.critical {
		color: var(--crit, #e5484d);
	}
	/* as: row: the label is a ReadoutRow, which brings its own ground */
	.lbl.rowlbl {
		background: none;
		padding: 0;
		margin-left: 0;
	}
	/* align: center: x is the label's middle */
	.lbl.center {
		transform: translateX(-50%);
		margin-left: 0;
	}
	.lbl.readout .num {
		color: var(--ink, #e6e6e6);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}
</style>
