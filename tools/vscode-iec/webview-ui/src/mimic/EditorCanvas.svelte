<script lang="ts">
	// The canvas: the same rendering as the runtime <Mimic> (kit components,
	// kit Pipe underlay, live bindings) plus the editing layer — selection,
	// grid-snapped drags, pipe drawing with port snapping, vertex handles,
	// placement ghosts. Gestures mutate LOCAL state while in flight; the op
	// commits on pointerup and the authoritative doc flows back from the
	// host. Between commit and confirmation the moved thing renders from an
	// optimistic "settled" override so it never flashes back.
	//
	// Connection points: resolvePorts() (ports.ts) resolves each equipment's
	// ports — instance override -> project sidecar entry for its component
	// -> registry's built-in default — as fractions of the rendered box;
	// eqPorts() is the one place that converts them to canvas pixels for
	// every snapping/attachment path below. Pipe ENDS that land on a
	// component ride along when it moves, with the neighboring vertex
	// adjusted to keep the run orthogonal — inferred from geometry, so the
	// .mimic.json stays plain [x, y] points and the runtime needs nothing
	// new. The ports EDITOR (ed.portsEdit) lets a user drag/add/delete a
	// component's own ports, writing to the shared sidecar by default or
	// the instance when explicitly toggled. The drag/add-by-edge-snap math
	// is shared with the standalone *.component.json editor — see
	// portsGestures.ts.
	import { flushSync } from 'svelte';
	import {
		builtinParts,
		makeGetPort,
		partRect,
		Pipe,
		pointsToPath,
		resolveBindings,
		resolvedPortDir,
		resolvePipeEndpoints,
		routedPoints
	} from '@joyautomation/nautilus-hmi';
	import type { GetPort, MimicEquipment, MimicPipe, MimicPipeAnchor, PartRect, PortDir } from '@joyautomation/nautilus-hmi';
	import { DEMO_PROPS, registry } from './registry';
	import { resolvePorts, type Port } from './ports';
	import { dropPort, fmtFraction, movePort, newPortAtFreeSlot, newPortAtPoint, nudgePort, roundPort, smallestPartAt } from './portsGestures';
	import { minInteriorPoints, nearestPort, resolveDraftFinish, type NamedPort } from './pipeDraft';
	import { suggestRoute, type ObstacleRect } from './autoroute';
	import { canvasBox, ed, GRID, postManifestOp, postOp, snap, type MimicOp, type Selection } from './mimicState.svelte';
	import { themeColorMode } from '../themeMode.svelte';
	import { clipEquipment, pasteOps, type MimicClip } from './mimicClip';
	import { readClip, typingTarget, writeClip } from '../clipboard';
	import PortsPanel from './PortsPanel.svelte';
	import UserIsland from './UserIsland.svelte';

	const doc = $derived(ed.doc);

	// ── scale: fit to the pane, or the user's zoom ──────────────────────────
	// The canvas opens fitted to the pane's width and follows it as the pane
	// resizes. Zooming (Ctrl+wheel / pinch around the pointer, Ctrl+= / -,
	// the corner buttons) takes over until Fit (Ctrl+0) hands back to the
	// pane — the same gestures as the ladder and SFC editors' ZoomPane.
	let wrap = $state<HTMLDivElement | null>(null);
	let fitScale = $state(1);
	/** null: follow the pane (fit width); a number: the user's zoom. */
	let zoom = $state<number | null>(null);
	const scale = $derived(zoom ?? fitScale);
	$effect(() => {
		if (!wrap || !doc) return;
		const el = wrap;
		const w = doc.canvas.width;
		const update = () => (fitScale = Math.min(1.5, Math.max(0.1, (el.clientWidth - 24) / w)));
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});

	const MIN_ZOOM = 0.1;
	const MAX_ZOOM = 4;
	const ZOOM_STEP = 1.2;
	/** Zoom to `next`, keeping the canvas point under (cx, cy) — client
	 * coordinates, the pane's centre when omitted — where it was. */
	function zoomAt(next: number, cx?: number, cy?: number) {
		const z0 = scale;
		const z1 = Math.round(Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next)) * 1000) / 1000;
		if (!wrap || !canvasEl || z1 === z0) return;
		const sr = wrap.getBoundingClientRect();
		const px = cx ?? sr.left + wrap.clientWidth / 2;
		const py = cy ?? sr.top + wrap.clientHeight / 2;
		const r0 = canvasEl.getBoundingClientRect();
		const ux = (px - r0.left) / z0;
		const uy = (py - r0.top) / z0;
		flushSync(() => (zoom = z1));
		const r1 = canvasEl.getBoundingClientRect();
		wrap.scrollLeft += r1.left + ux * z1 - px;
		wrap.scrollTop += r1.top + uy * z1 - py;
	}
	const zoomIn = () => zoomAt(scale * ZOOM_STEP);
	const zoomOut = () => zoomAt(scale / ZOOM_STEP);
	function zoomFit() {
		zoom = null;
		if (wrap) wrap.scrollLeft = wrap.scrollTop = 0;
	}

	// Ctrl/Cmd+wheel and pinch (Chromium reports a pinch as ctrl+wheel).
	// Non-passive: preventDefault keeps VS Code's own zoom out of it.
	$effect(() => {
		const el = wrap;
		if (!el) return;
		const onWheel = (ev: WheelEvent) => {
			if (!(ev.ctrlKey || ev.metaKey)) return;
			ev.preventDefault();
			const unit = ev.deltaMode === 1 ? 16 : ev.deltaMode === 2 ? 800 : 1;
			const d = Math.max(-60, Math.min(60, ev.deltaY * unit));
			zoomAt(scale * Math.exp(-d * 0.0025), ev.clientX, ev.clientY);
		};
		el.addEventListener('wheel', onWheel, { passive: false });
		// Autoscroll's middle-click cursor would fight the pan.
		const noAuto = (ev: MouseEvent) => ev.button === 1 && ev.preventDefault();
		el.addEventListener('mousedown', noAuto);
		return () => {
			el.removeEventListener('wheel', onWheel);
			el.removeEventListener('mousedown', noAuto);
		};
	});

	/** Ctrl+= / Ctrl+- / Ctrl+0. On the document, so it runs (and stops
	 * the key) before VS Code's window-level listener would replay it in
	 * the workbench and zoom the whole window. */
	function onZoomKey(ev: KeyboardEvent) {
		if (!(ev.ctrlKey || ev.metaKey) || ev.altKey || !doc || typingTarget(ev)) return;
		let acted = true;
		if (ev.key === '=' || ev.key === '+' || ev.code === 'NumpadAdd') zoomIn();
		else if (ev.key === '-' || ev.key === '_' || ev.code === 'NumpadSubtract') zoomOut();
		else if (ev.key === '0' || ev.code === 'Numpad0') zoomFit();
		else acted = false;
		if (acted) {
			ev.preventDefault();
			ev.stopPropagation();
		}
	}

	// Middle-button drag pans (left-drag belongs to the editing gestures).
	let pan: { x: number; y: number; sl: number; st: number } | null = null;
	function panDown(ev: PointerEvent) {
		if (ev.button !== 1 || !wrap) return;
		ev.preventDefault();
		pan = { x: ev.clientX, y: ev.clientY, sl: wrap.scrollLeft, st: wrap.scrollTop };
		wrap.setPointerCapture?.(ev.pointerId);
	}
	function panMove(ev: PointerEvent) {
		if (!pan || !wrap) return;
		wrap.scrollLeft = pan.sl - (ev.clientX - pan.x);
		wrap.scrollTop = pan.st - (ev.clientY - pan.y);
	}
	const panUp = () => (pan = null);

	let canvasEl = $state<HTMLDivElement | null>(null);
	function pt(e: { clientX: number; clientY: number }): [number, number] {
		const r = canvasEl!.getBoundingClientRect();
		return [(e.clientX - r.left) / scale, (e.clientY - r.top) / scale];
	}

	// ── equipment boxes & ports ─────────────────────────────────────────────
	// Rendered sizes come from the DOM (offsetWidth is in canvas units —
	// the scale transform doesn't affect it).
	const eqEls = new Map<string, HTMLElement>();
	/** Bumped whenever an equipment's rendered box changes size — read by
	 * pipeRouteInput() so an anchored pipe follows a resize (a width typed
	 * in the panel, a resize-handle drag, a component that grows with its
	 * props) without waiting for some other change to re-render it. */
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

	/** Where a named part of `eq`'s drawing sits in its box: measured off
	 * the DOM (the element marked `data-part`), else the built-in drawing's
	 * own layout (hmi's builtinParts) when it hasn't painted yet. */
	function partOf(eq: MimicEquipment, part: string): PartRect | undefined {
		const b = eqBox(eq);
		return partRect(eqEls.get(eq.id), part) ?? builtinParts(eq.component, b, eq)?.[part];
	}

	/** The rectangle a port's fractions are OF, in canvas pixels: its part
	 * when it names one the drawing has, else the whole box. */
	function portFrame(eq: MimicEquipment, port: Pick<Port, 'part'>): { x: number; y: number; w: number; h: number } {
		const b = eqBox(eq);
		const r = port.part ? partOf(eq, port.part) : undefined;
		return r ? { x: b.x + r.x, y: b.y + r.y, w: r.w, h: r.h } : b;
	}

	/** Every named part of `eq`'s rendered drawing, as [name, canvas frame]. */
	function partsOf(eq: MimicEquipment): [string, { x: number; y: number; w: number; h: number }][] {
		const el = eqEls.get(eq.id);
		if (!el) return [];
		const names = new Set([...el.querySelectorAll<Element>('[data-part]')].map((n) => n.getAttribute('data-part')!));
		return [...names].map((name) => [name, portFrame(eq, { part: name })]);
	}

	/** `eq`'s rendered box — position from dispEq()'s SAME live-drag/settled
	 * override `eq`'s own div uses for its `left/top` style (so a pipe
	 * anchored to this equipment's port re-derives its position from the
	 * box LIVE, in step with the equipment, while an 'eq' drag is still in
	 * flight — not just after it lands), size DOM-measured. */
	function eqBox(eq: MimicEquipment): { x: number; y: number; w: number; h: number } {
		const el = eqEls.get(eq.id);
		const pos = dispEq(eq);
		const size = dispSize(eq);
		return {
			x: pos.x,
			y: pos.y,
			w: el?.offsetWidth || size.width || 100,
			h: el?.offsetHeight || size.height || 80
		};
	}

	// PropsPanel's detach / Re-route resolve ports against these same boxes.
	$effect(() => {
		canvasBox.measure = (id) => {
			const eq = (doc?.equipment ?? []).find((e) => e.id === id);
			return eq ? eqBox(eq) : undefined;
		};
		return () => {
			canvasBox.measure = null;
		};
	});

	/** A resolved port projected into canvas pixels — name + source
	 * fractions + effective (explicit or inferred) exit direction carried
	 * through for the hover tooltip and the direction tick. */
	type PortPx = { name: string; x: number; y: number; fx: number; fy: number; dir?: PortDir };

	/** Resolved connection points (instance override -> sidecar -> built-in
	 * default; see ports.ts), in canvas pixels. The single call site every
	 * snapping/attachment path below goes through. */
	function eqPorts(eq: MimicEquipment): PortPx[] {
		const spec = resolvePorts(eq, ed.manifest);
		if (!spec.length) return [];
		return spec.map((p) => {
			const b = portFrame(eq, p);
			return {
				name: p.name,
				x: Math.round(b.x + p.x * b.w),
				y: Math.round(b.y + p.y * b.h),
				fx: p.x,
				fy: p.y,
				dir: resolvedPortDir(p)
			};
		});
	}

	/** Ports currently shown by the ports editor for `eq` — the resolved set,
	 * with the in-flight drag (if any) substituted in — as canvas pixels. */
	function editingPortsPx(eq: MimicEquipment): PortPx[] {
		const fr = resolvePorts(eq, ed.manifest).map((p) => ({ ...p }));
		if (drag?.kind === 'port' && drag.id === eq.id) fr[drag.index] = movePort(fr[drag.index], { x: drag.fx, y: drag.fy, part: drag.part });
		return fr.map((p) => {
			const b = portFrame(eq, p);
			return {
				name: p.name,
				x: Math.round(b.x + p.x * b.w),
				y: Math.round(b.y + p.y * b.h),
				fx: p.x,
				fy: p.y,
				dir: resolvedPortDir(p)
			};
		});
	}

	/** Direction-tick endpoint (unit vector * length) for a resolved port —
	 * the small arrow drawn off a port dot showing its effective exit
	 * direction (Feature 1). Shared constant with PreviewApp.svelte's own
	 * copy (kept file-local rather than exported: it's presentation, not a
	 * document-shape concern like PORT_STUB). */
	const TICK_VEC: Record<PortDir, [number, number]> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] };
	const TICK_LEN = 8;
	function tickEnd(port: PortPx): [number, number] {
		if (!port.dir) return [port.x, port.y];
		const [dx, dy] = TICK_VEC[port.dir];
		return [port.x + dx * TICK_LEN, port.y + dy * TICK_LEN];
	}

	/** Commit a full ports list for `eq` to wherever the ports editor is
	 * currently targeting (manifest = every instance of the component;
	 * instance = just this one). */
	function commitPorts(eq: MimicEquipment, ports: Port[]) {
		const rounded = ports.map(roundPort);
		if (ed.portsEdit?.target === 'instance') {
			postOp({ type: 'setEquipmentPorts', id: eq.id, ports: rounded });
		} else {
			postManifestOp({ type: 'setComponentPorts', component: eq.component, ports: rounded });
		}
	}

	function portDelete(eq: MimicEquipment, i: number) {
		commitPorts(eq, resolvePorts(eq, ed.manifest).filter((_, j) => j !== i));
	}

	/** Add a port from a click on the equipment's box (ports-edit mode) — see
	 * portsGestures.ts's newPortAtPoint for the projection/snap/auto-name math
	 * (shared with the standalone *.component.json editor). */
	function addPort(eq: MimicEquipment, raw: [number, number]) {
		const existing = resolvePorts(eq, ed.manifest);
		// On a part of the drawing (a vessel, a valve body), the new port
		// anchors to that part, so it stays on it when the equipment is
		// resized; elsewhere it's a fraction of the whole box as before.
		const hit = smallestPartAt(partsOf(eq), raw);
		const port = hit ? { ...newPortAtPoint(hit[1], raw, existing), part: hit[0] } : newPortAtPoint(eqBox(eq), raw, existing);
		commitPorts(eq, [...existing, port]);
	}

	const PORT_SNAP = 14;
	/** A cursor point -> the nearest port within snap radius, WITH its owning
	 * equipment id + port name (not just its coordinates) — the seam pipe
	 * drawing/dragging use to record an anchor rather than a raw point.
	 * (NamedPort's shape is imported from pipeDraft.ts — the one other place
	 * that needs to reason about a "point that might be a port".) */
	function portSnapNamed(p: [number, number]): NamedPort | null {
		const all: NamedPort[] = [];
		for (const eq of doc?.equipment ?? []) {
			for (const port of eqPorts(eq)) all.push({ equip: eq.id, port: port.name, x: port.x, y: port.y });
		}
		return nearestPort(p, all, PORT_SNAP);
	}
	function portSnap(p: [number, number]): [number, number] | null {
		const n = portSnapNamed(p);
		return n ? [n.x, n.y] : null;
	}

	// ── pipe anchors (Feature 2) ────────────────────────────────────────────
	/** Resolves an anchor ref (equip id + port name) to an absolute canvas
	 * point + effective dir, for resolvePipeEndpoints()/routedPoints() — the
	 * SAME resolution eqPorts() uses (instance -> sidecar -> builtin ports,
	 * DOM-measured box), so a pipe anchor lands exactly on its port dot. */
	const getPort: GetPort = makeGetPort(
		(id) => {
			const eq = (doc?.equipment ?? []).find((e) => e.id === id);
			return eq ? eqBox(eq) : undefined;
		},
		(id) => {
			const eq = (doc?.equipment ?? []).find((e) => e.id === id);
			return eq ? resolvePorts(eq, ed.manifest) : undefined;
		},
		(id, part) => {
			const eq = (doc?.equipment ?? []).find((e) => e.id === id);
			return eq ? partOf(eq, part) : undefined;
		}
	);

	/** Keep-out rectangles for autoroute.ts's obstacle avoidance: every
	 * equipment box — the pipe's OWN end equipment included (suggestRoute's
	 * `ends` lets the route leave a port outward first, so a side port's
	 * vessel is gone around, not through) — expanded by a fixed margin so a
	 * suggested route clears equipment by a comfortable gap rather than
	 * grazing it. */
	const ROUTE_MARGIN = 16;
	function keepOut(eq: MimicEquipment): ObstacleRect {
		const b = eqBox(eq);
		return { x: b.x - ROUTE_MARGIN, y: b.y - ROUTE_MARGIN, w: b.w + 2 * ROUTE_MARGIN, h: b.h + 2 * ROUTE_MARGIN };
	}
	function obstaclesFor(): ObstacleRect[] {
		return (doc?.equipment ?? []).map(keepOut);
	}
	function keepOutOf(id: string): ObstacleRect | undefined {
		const eq = (doc?.equipment ?? []).find((e) => e.id === id);
		return eq ? keepOut(eq) : undefined;
	}

	/** How many interior points a pipe needs given its CURRENT from/to — thin
	 * wrapper around pipeDraft.ts's minInteriorPoints (itself a mirror of
	 * mimicOps.ts's; that reducer copy is the authority) for the vertex-
	 * delete cascade threshold below. */
	function minInteriorFloor(p: Pick<MimicPipe, 'from' | 'to'>): number {
		return minInteriorPoints(!!p.from, !!p.to);
	}

	/** Paint order for the pipe network — walls, then bores, then flow
	 * overlays, matching the kit's Mimic (see the junction note in the kit's
	 * Pipe.svelte). */
	const PIPE_LAYERS = ['wall', 'bore', 'flow'] as const;

	/** `{points, from, to}` to feed routedPoints()/resolvePipeEndpoints() for
	 * `p`, with an in-flight 'anchor' drag on THIS pipe live-substituted in.
	 * The substitution mirrors dropAnchor() EXACTLY — same anchor state, same
	 * interior-point set — so the live drag preview routes through the SAME
	 * pipeline (routedPoints: stubs + orthogonal corners) as what pointer-up
	 * commits. When the drag is snapped onto a port, that end previews as
	 * ANCHORED (stub and all); off any port it previews as a plain moved point.
	 * Either way the OTHER end's anchor still resolves normally. */
	function pipeRouteInput(p: MimicPipe): Pick<MimicPipe, 'points' | 'routing' | 'from' | 'to'> {
		void boxVersion;
		const interior = dispPts(p);
		if (drag?.kind === 'anchor' && drag.id === p.id) {
			const live: [number, number] = [drag.x, drag.y];
			const wasAnchored = drag.end === 'from' ? !!p.from : !!p.to;
			// The interior points MINUS the dragged terminal's own stored point
			// when this end wasn't anchored (its endpoint lives in `points`, not
			// as an anchor) — exactly dropAnchor()'s shift()/pop().
			const base = wasAnchored ? interior : drag.end === 'from' ? interior.slice(1) : interior.slice(0, -1);
			if (drag.port) {
				// Snapping onto a port: that end is anchored, supplying its own
				// endpoint — no live interior point (dropAnchor's attach path).
				return drag.end === 'from'
					? { points: base, routing: p.routing, from: drag.port, to: p.to }
					: { points: base, routing: p.routing, from: p.from, to: drag.port };
			}
			// Off any port: a plain moved/detached endpoint at the live cursor.
			return drag.end === 'from'
				? { points: [live, ...base], routing: p.routing, from: undefined, to: p.to }
				: { points: [...base, live], routing: p.routing, from: p.from, to: undefined };
		}
		return { points: interior, routing: p.routing, from: p.from, to: p.to };
	}

	/** The pipe's full, editable vertex list — anchors resolved to absolute
	 * positions and spliced onto the interior points (NO dir stubs / no
	 * orthogonal corners — those are cosmetic, not vertices a user drags).
	 * Index 0 is the start handle (an anchor dot when `p.from` is set, else
	 * a plain point); the last index is the end handle, same rule. Every
	 * vertex-handle gesture below (vtxDown/midDown/vtxDelete) works in this
	 * FULL index space and converts to/from the interior (p.points) index
	 * with `p.from ? 1 : 0` as the offset. */
	function pipeHandlePoints(p: MimicPipe): [number, number][] {
		return resolvePipeEndpoints(pipeRouteInput(p), getPort).points;
	}

	/** True when `p` has an anchor (from and/or to) that doesn't resolve to
	 * a real equipment/port right now — an unknown ref, whether hand-written
	 * or left behind some other way. Still renders (resolvePipeEndpoints
	 * falls back rather than refusing); this just flags it visually so
	 * PropsPanel's fix affordance has something to point at. */
	function pipeFlagged(p: MimicPipe): boolean {
		const fromBad = !!p.from && !getPort(p.from.equip, p.from.port);
		const toBad = !!p.to && !getPort(p.to.equip, p.to.port);
		return fromBad || toBad;
	}

	// ── pipe attachment inference (legacy, un-anchored pipes only) ─────────
	/** A pipe END sitting on/in this equipment (± margin). adjust says which
	 * coordinate of the NEIGHBOR vertex follows to keep the run orthogonal.
	 * Superseded for a pipe end that's EXPLICITLY anchored (Feature 2) — that
	 * end already moves with its equipment via derivation, with no batched
	 * point-shift needed, so this proximity heuristic skips it entirely. */
	type Attach = { pipeId: string; index: number; adjust: 'x' | 'y' | null };

	function attachmentsFor(eq: MimicEquipment): Attach[] {
		if (!doc) return [];
		const b = eqBox(eq);
		const M = 8;
		const out: Attach[] = [];
		for (const p of doc.pipes ?? []) {
			if (p.points.length < 2) continue;
			for (const index of [0, p.points.length - 1]) {
				if (index === 0 && p.from) continue;
				if (index === p.points.length - 1 && p.to) continue;
				const q = p.points[index];
				if (q[0] < b.x - M || q[0] > b.x + b.w + M || q[1] < b.y - M || q[1] > b.y + b.h + M) continue;
				const n = p.points[index === 0 ? 1 : p.points.length - 2];
				const adjust: Attach['adjust'] =
					n[1] === q[1] && n[0] !== q[0] ? 'y' : n[0] === q[0] && n[1] !== q[1] ? 'x' : null;
				out.push({ pipeId: p.id, index, adjust });
			}
		}
		// A 2-point pipe with both ends on this equipment: translating both
		// ends is right; neighbor-adjust would fight it.
		for (const a of out) {
			const twin = out.find((o) => o !== a && o.pipeId === a.pipeId);
			const len = (doc.pipes ?? []).find((p) => p.id === a.pipeId)?.points.length ?? 0;
			if (twin && len === 2) a.adjust = null;
		}
		return out;
	}

	function shiftedPoints(p: MimicPipe, atts: Attach[], dx: number, dy: number): [number, number][] {
		const pts = p.points.map((q) => [...q] as [number, number]);
		for (const a of atts) {
			if (a.pipeId !== p.id) continue;
			const q = pts[a.index];
			q[0] += dx;
			q[1] += dy;
			const n = pts[a.index === 0 ? 1 : pts.length - 2];
			if (a.adjust === 'y') n[1] = q[1];
			else if (a.adjust === 'x') n[0] = q[0];
		}
		return pts;
	}

	/** Every pipe anchored (either end) to `eqId`, patched to materialize
	 * that end at its CURRENT derived position and clear the anchor — the
	 * "no dangling refs from editor actions" half of Feature 2's delete
	 * rule (hand-written JSON with a dangling ref still renders flagged and
	 * fixable; this is what keeps the EDITOR from ever creating one). Built
	 * from the doc's state BEFORE the equipment is removed (so the anchor
	 * still resolves), meant to run in the SAME batch as deleteEquipment. */
	function materializeAnchorsFor(eqId: string | string[]): MimicOp[] {
		const gone = new Set(Array.isArray(eqId) ? eqId : [eqId]);
		const ops: MimicOp[] = [];
		for (const p of doc?.pipes ?? []) {
			const fromHit = !!p.from && gone.has(p.from.equip);
			const toHit = !!p.to && gone.has(p.to.equip);
			if (!fromHit && !toHit) continue;
			const full = pipeHandlePoints(p);
			const pts = p.points.map((q) => [...q] as [number, number]);
			const patch: Record<string, unknown> = {};
			if (fromHit) {
				pts.unshift(full[0]);
				patch.from = null;
			}
			if (toHit) {
				pts.push(full[full.length - 1]);
				patch.to = null;
			}
			patch.points = pts;
			ops.push({ type: 'updatePipe', id: p.id, patch });
		}
		return ops;
	}

	/** Delete equipment as ONE batch with any anchor materialization it
	 * requires — so a delete gesture never leaves a dangling anchor ref
	 * behind, per Feature 2's design rule. */
	function deleteEquipmentWithAnchors(eqId: string | string[], labels: number[] = []) {
		const ids = Array.isArray(eqId) ? eqId : [eqId];
		// One pass over the whole set: a pipe between two deleted instances
		// materializes both ends in ONE patch (two per-instance patches would
		// each start from the original points and the second would win).
		const matOps = materializeAnchorsFor(ids);
		const dels: MimicOp[] = ids.map((id) => ({ type: 'deleteEquipment', id }));
		// Highest index first, so each delete leaves the next index in place.
		for (const index of [...labels].sort((a, b) => b - a)) dels.push({ type: 'deleteLabel', index });
		const ops = [...matOps, ...dels];
		postOp(ops.length > 1 ? { type: 'batch', ops } : ops[0]);
	}

	// ── clipboard: copy / cut / paste / duplicate, select all ─────────────
	/** The equipment ids the current selection covers. */
	function selEquipIds(): string[] {
		const s = ed.selection;
		return s?.kind === 'equipment' ? [s.id] : s?.kind === 'multi' ? [...s.ids] : [];
	}
	function selectEquip(ids: string[]) {
		selectItems(ids, []);
	}
	/** The label indices the current selection covers. */
	function selLabels(): number[] {
		const s = ed.selection;
		return s?.kind === 'label' ? [s.index] : s?.kind === 'multi' ? [...(s.labels ?? [])] : [];
	}
	/** Select these equipment ids and label indices: one item is a plain
	 * equipment or label selection, more are a 'multi'. */
	function selectItems(ids: string[], labels: number[]) {
		if (ids.length + labels.length > 1) ed.selection = labels.length ? { kind: 'multi', ids, labels } : { kind: 'multi', ids };
		else if (ids.length) ed.selection = { kind: 'equipment', id: ids[0] };
		else if (labels.length) ed.selection = { kind: 'label', index: labels[0] };
		else ed.selection = null;
	}
	/** Shift- or Ctrl/Cmd-click: add `item` to the selection or take it out.
	 * A Shift-click mid a pipe-point selection is a missed vertex (BUG 1,
	 * see pipeDown) and changes nothing. */
	function toggleItem(e: PointerEvent, item: { id: string } | { index: number }): boolean {
		if (!e.shiftKey && !e.ctrlKey && !e.metaKey) return false;
		if (e.shiftKey && ed.selection?.kind === 'nodes') return true;
		const ids = selEquipIds();
		const labels = selLabels();
		if ('id' in item) selectItems(ids.includes(item.id) ? ids.filter((x) => x !== item.id) : [...ids, item.id], labels);
		else selectItems(ids, labels.includes(item.index) ? labels.filter((x) => x !== item.index) : [...labels, item.index]);
		return true;
	}
	/** A press on an item of a several-item selection grabs them all. A
	 * plain press released without a move then selects just that item; a
	 * Shift/Ctrl press (`collapse` false) just added it and leaves it be. */
	function groupDown(e: PointerEvent, hit: Selection, x: number, y: number, collapse = true): boolean {
		const s = ed.selection;
		if (s?.kind !== 'multi' || !doc) return false;
		const inSel = hit.kind === 'equipment' ? s.ids.includes(hit.id) : hit.kind === 'label' && (s.labels ?? []).includes(hit.index);
		if (!inSel) return false;
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
		const [px, py] = pt(e);
		const eqs = (doc.equipment ?? []).filter((q) => s.ids.includes(q.id));
		drag = {
			kind: 'group',
			ids: [...s.ids],
			labels: [...(s.labels ?? [])],
			hit: collapse ? hit : null,
			gx: px - x,
			gy: py - y,
			ax: x,
			ay: y,
			dx: 0,
			dy: 0,
			atts: eqs.flatMap((q) => attachmentsFor(q)),
			moved: false
		};
		return true;
	}
	// Repeated pastes of the same clip cascade instead of stacking exactly.
	let pasteKey = '';
	let pasteN = 0;
	function pasteClip(clip: MimicClip) {
		if (!doc || !clip.equipment?.length) return;
		const key = JSON.stringify(clip);
		pasteN = key === pasteKey ? pasteN + 1 : 1;
		pasteKey = key;
		const { op, ids } = pasteOps(doc, clip, GRID * 2 * pasteN, GRID * 2 * pasteN);
		postOp(op);
		selectEquip(ids);
	}
	function copySel(): boolean {
		if (!doc) return false;
		const clip = clipEquipment(doc, selEquipIds());
		if (!clip) return false;
		writeClip('mimic', clip);
		pasteKey = JSON.stringify(clip);
		pasteN = 0;
		return true;
	}
	function cutSel(): boolean {
		if (!copySel()) return false;
		const ids = selEquipIds();
		ed.selection = null;
		deleteEquipmentWithAnchors(ids);
		pasteN = -1; // a cut's first paste lands where the originals were
		return true;
	}
	function duplicateSel(): boolean {
		if (!doc) return false;
		const clip = clipEquipment(doc, selEquipIds());
		if (!clip) return false;
		pasteClip(clip);
		return true;
	}
	async function pasteFromClipboard() {
		const clip = await readClip<MimicClip>('mimic');
		if (clip) pasteClip(clip);
	}

	// ── in-flight gestures ──────────────────────────────────────────────────
	type Drag =
		| { kind: 'eq'; id: string; dx: number; dy: number; ox: number; oy: number; x: number; y: number; atts: Attach[]; moved: boolean }
		| { kind: 'label'; index: number; dx: number; dy: number; x: number; y: number; moved: boolean }
		/** Dragging a several-item selection by one of its items (`hit`):
		 * `ax`/`ay` is that item's position at grab, `gx`/`gy` the pointer's
		 * offset from it, `dx`/`dy` the live (grid-snapped) move. */
		| { kind: 'group'; ids: string[]; labels: number[]; hit: Selection | null; gx: number; gy: number; ax: number; ay: number; dx: number; dy: number; atts: Attach[]; moved: boolean }
		/** `index` is an INTERIOR index into the pipe's stored `points` — see
		 * pipeHandlePoints()'s doc comment for the full-vs-interior index split. */
		| { kind: 'vtx'; id: string; index: number; pts: [number, number][]; moved: boolean }
		| { kind: 'port'; id: string; index: number; fx: number; fy: number; part?: string; moved: boolean }
		/** Dragging a resize handle on the selected equipment: `edge` says
		 * which sides move (e = right, s = bottom, se = both); `w0`/`h0` and
		 * `px`/`py` are the box and pointer at grab time. */
		| { kind: 'resize'; id: string; edge: 'e' | 's' | 'se'; px: number; py: number; w0: number; h0: number; w: number; h: number; moved: boolean }
		/** Shift-drag on empty canvas: a selection box from (x0, y0) to (x1,
		 * y1). What lies wholly inside it joins `base` (the selection at the
		 * press); `orig` is restored while the box holds nothing. */
		| { kind: 'box'; x0: number; y0: number; x1: number; y1: number; base: { ids: string[]; labels: number[] }; orig: Selection | null; moved: boolean }
		/** Dragging the canvas's own right/bottom edge or corner — the same
		 * shape as 'resize', committed as a setCanvas. */
		| { kind: 'canvas'; edge: 'e' | 's' | 'se'; px: number; py: number; w0: number; h0: number; w: number; h: number; moved: boolean }
		/** Dragging a pipe's terminal (start/end) handle — Feature 2's
		 * attach/detach gesture. `x`/`y` track the live cursor (port-snapped
		 * when near one); `port` is the port that snap currently lands on (null
		 * off any port) so the live preview can route EXACTLY what the drop will
		 * commit (a stub + orthogonal corner when it'll attach). Resolved on
		 * drop (onup) by re-checking that same position. */
		| { kind: 'anchor'; id: string; end: 'from' | 'to'; x: number; y: number; port: MimicPipeAnchor | null; moved: boolean };
	let drag = $state<Drag | null>(null);
	/** Row selection for the ports-edit side panel (PortsPanel, below) —
	 * mirrors PreviewApp.svelte's `selected`. Reset whenever ed.portsEdit
	 * changes (see the $effect near the template). */
	let portsSelected = $state<number | null>(null);

	/** Committed-but-unconfirmed positions: rendered until the doc catches
	 * up (or a refusal times out) so nothing flashes back on pointerup. */
	let settled = $state<null | {
		eq: Record<string, { x: number; y: number }>;
		pipes: Record<string, [number, number][]>;
		labels: Record<number, { x: number; y: number }>;
		sizes: Record<string, { width: number; height: number }>;
		canvas: { width: number; height: number } | null;
	}>(null);
	let settleTimer: ReturnType<typeof setTimeout> | undefined;
	function settle(part: {
		eq?: Record<string, { x: number; y: number }>;
		pipes?: Record<string, [number, number][]>;
		labels?: Record<number, { x: number; y: number }>;
		sizes?: Record<string, { width: number; height: number }>;
		canvas?: { width: number; height: number };
	}) {
		settled = { eq: {}, pipes: {}, labels: {}, sizes: {}, canvas: null, ...part };
		clearTimeout(settleTimer);
		settleTimer = setTimeout(() => (settled = null), 1500);
	}
	$effect(() => {
		const d = ed.doc;
		if (!d || !settled) return;
		const s = settled;
		const ok =
			Object.entries(s.eq).every(([id, p]) => {
				const e = (d.equipment ?? []).find((e) => e.id === id);
				return e && e.x === p.x && e.y === p.y;
			}) &&
			Object.entries(s.pipes).every(([id, pts]) => {
				const p = (d.pipes ?? []).find((p) => p.id === id);
				return p && JSON.stringify(p.points) === JSON.stringify(pts);
			}) &&
			Object.entries(s.labels).every(([i, p]) => {
				const l = (d.labels ?? [])[+i];
				return l && l.x === p.x && l.y === p.y;
			}) &&
			Object.entries(s.sizes).every(([id, z]) => {
				const e = (d.equipment ?? []).find((e) => e.id === id);
				return e && e.width === z.width && e.height === z.height;
			}) &&
			(!s.canvas || (d.canvas.width === s.canvas.width && d.canvas.height === s.canvas.height));
		if (ok) {
			settled = null;
			clearTimeout(settleTimer);
		}
	});

	/** Pipe-drawing draft (tool = pipe). */
	let draft = $state<[number, number][]>([]);
	/** Snapped cursor position for ghosts and the draft's rubber segment. */
	let cursor = $state<[number, number] | null>(null);
	/** Feature 2: starting/ending a draw on a port dot records an anchor
	 * instead of a raw point. `draftFrom` locks in on the draft's FIRST
	 * point; `draftLastSnap` tracks whichever port (if any) the MOST
	 * recently placed point landed on, so finishPipe() can use it as `to`
	 * (the last point is always the one about to become the end). */
	let draftFrom = $state<MimicPipeAnchor | null>(null);
	let draftLastSnap = $state<MimicPipeAnchor | null>(null);
	/** The port currently under the cursor while drawing — highlighted on
	 * the canvas as feedback that the next click/finish will anchor there. */
	let hoverPort = $state<NamedPort | null>(null);

	const dispEq = (eq: MimicEquipment): { x: number; y: number } =>
		drag?.kind === 'eq' && drag.id === eq.id
			? { x: drag.x, y: drag.y }
			: drag?.kind === 'group' && drag.ids.includes(eq.id)
				? { x: eq.x + drag.dx, y: eq.y + drag.dy }
				: (settled?.eq[eq.id] ?? { x: eq.x, y: eq.y });
	/** `eq`'s width/height as drawn: a live resize, else a committed one the
	 * doc hasn't echoed yet, else the doc's own (either may be unset). */
	const dispSize = (eq: MimicEquipment): { width?: number; height?: number } =>
		drag?.kind === 'resize' && drag.id === eq.id
			? { width: drag.w, height: drag.h }
			: (settled?.sizes[eq.id] ?? { width: eq.width, height: eq.height });
	/** The canvas size as drawn: a live edge drag, else a committed one the
	 * doc hasn't echoed yet, else the doc's own. */
	const canvasSize = $derived(
		drag?.kind === 'canvas'
			? { width: drag.w, height: drag.h }
			: (settled?.canvas ?? doc?.canvas ?? { width: 0, height: 0 })
	);
	const cw = $derived(canvasSize.width);
	const ch = $derived(canvasSize.height);
	const dispLabel = (i: number, l: { x: number; y: number }): { x: number; y: number } =>
		drag?.kind === 'label' && drag.index === i
			? { x: drag.x, y: drag.y }
			: drag?.kind === 'group' && drag.labels.includes(i)
				? { x: l.x + drag.dx, y: l.y + drag.dy }
				: (settled?.labels[i] ?? { x: l.x, y: l.y });
	// A bound label's live value, formatted the way the runtime <Mimic>'s
	// labelValue() does (same resolveBindings() path, `decimals` default 1),
	// or null when the label has no bind or its tag hasn't resolved to a
	// finite number — then the label is its text alone, as it always was
	// here, rather than the runtime's '—' placeholder on a dead canvas.
	const labelLive = (l: { bind?: string; decimals?: number }): string | null => {
		if (!l.bind) return null;
		const v = resolveBindings({ value: l.bind }, ed.tags ?? {}).value;
		return typeof v === 'number' && isFinite(v) ? v.toFixed(l.decimals ?? 1) : null;
	};
	const dispPts = (p: MimicPipe): [number, number][] => {
		if (drag?.kind === 'vtx' && drag.id === p.id) return drag.pts;
		if (drag?.kind === 'eq' && drag.atts.some((a) => a.pipeId === p.id)) {
			return shiftedPoints(p, drag.atts, drag.x - drag.ox, drag.y - drag.oy);
		}
		if (drag?.kind === 'group' && (drag.dx || drag.dy)) {
			const pts = groupPipePoints(p, drag.ids, drag.atts, drag.dx, drag.dy);
			if (pts) return pts;
		}
		return settled?.pipes[p.id] ?? p.points;
	};

	const eqProps = (eq: MimicEquipment): Record<string, unknown> => {
		const size = dispSize(eq);
		return {
			look: doc?.look ?? 'isa101',
			...(DEMO_PROPS[eq.component] ?? {}),
			...(size.width !== undefined ? { width: size.width } : {}),
			...(size.height !== undefined ? { height: size.height } : {}),
			...(eq.label !== undefined ? { label: eq.label } : {}),
			...(eq.props ?? {}),
			...resolveBindings(eq.bind, ed.tags ?? {})
		};
	};

	// ── equipment / label drags ─────────────────────────────────────────────
	function eqDown(e: PointerEvent, eq: MimicEquipment) {
		if (ed.tool !== 'select' || e.button !== 0) return;
		e.stopPropagation();
		// Shift/Ctrl/Cmd-click toggles this instance in the selection;
		// toggleItem keeps BUG 1's guard for a node multi-selection. Added to
		// a several-item selection, a drag from the same press moves them all.
		if (toggleItem(e, { id: eq.id })) {
			groupDown(e, { kind: 'equipment', id: eq.id }, eq.x, eq.y, false);
			return;
		}
		if (groupDown(e, { kind: 'equipment', id: eq.id }, eq.x, eq.y)) return;
		ed.selection = { kind: 'equipment', id: eq.id };
		if (ed.portsEdit?.id === eq.id) return; // ports editor owns clicks on this box (see eqDblclick)
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
		const [px, py] = pt(e);
		// Attachments are computed at grab time, against the doc's geometry.
		drag = {
			kind: 'eq',
			id: eq.id,
			dx: px - eq.x,
			dy: py - eq.y,
			ox: eq.x,
			oy: eq.y,
			x: eq.x,
			y: eq.y,
			atts: attachmentsFor(eq),
			moved: false
		};
	}

	/** Commit an equipment move + its attached pipe ends as ONE batch op. */
	function postEqMove(eq: MimicEquipment, atts: Attach[], nx: number, ny: number) {
		if (!doc) return;
		const dx = nx - eq.x;
		const dy = ny - eq.y;
		const ops: MimicOp[] = [{ type: 'moveEquipment', id: eq.id, x: nx, y: ny }];
		const pipeSettle: Record<string, [number, number][]> = {};
		for (const pid of new Set(atts.map((a) => a.pipeId))) {
			const p = (doc.pipes ?? []).find((p) => p.id === pid);
			if (!p) continue;
			const pts = shiftedPoints(p, atts, dx, dy);
			ops.push({ type: 'setPipePoints', id: pid, points: pts });
			pipeSettle[pid] = pts;
		}
		postOp(ops.length > 1 ? { type: 'batch', ops } : ops[0]);
		settle({ eq: { [eq.id]: { x: nx, y: ny } }, pipes: pipeSettle });
	}

	/** `p`'s points with the equipment `ids` moved by (dx, dy), or null when
	 * the move doesn't touch it. A pipe whose two ends are both on moved
	 * equipment (anchored or attached) moves whole, bends and all; one with
	 * a single end on them stretches like a single-instance move. */
	function groupPipePoints(p: MimicPipe, ids: string[], atts: Attach[], dx: number, dy: number): [number, number][] | null {
		const last = p.points.length - 1;
		const on = (anchor: MimicPipeAnchor | undefined, index: number) =>
			anchor ? ids.includes(anchor.equip) : atts.some((a) => a.pipeId === p.id && a.index === index);
		const from = on(p.from, 0);
		const to = on(p.to, last);
		if (from && to) return p.points.map(([x, y]) => [x + dx, y + dy]);
		if (atts.some((a) => a.pipeId === p.id)) return shiftedPoints(p, atts, dx, dy);
		return null;
	}

	/** Move several instances and labels as ONE batch: every move, plus each
	 * pipe the move touches (see groupPipePoints). */
	function moveGroup(ids: string[], labels: number[], dx: number, dy: number, atts?: Attach[]) {
		if (!doc || (!dx && !dy)) return;
		const eqs = (doc.equipment ?? []).filter((q) => ids.includes(q.id));
		atts ??= eqs.flatMap((q) => attachmentsFor(q));
		const ops: MimicOp[] = eqs.map((q) => ({ type: 'moveEquipment', id: q.id, x: q.x + dx, y: q.y + dy }));
		const labelSettle: Record<number, { x: number; y: number }> = {};
		for (const i of labels) {
			const l = (doc.labels ?? [])[i];
			if (!l) continue;
			ops.push({ type: 'updateLabel', index: i, patch: { x: l.x + dx, y: l.y + dy } });
			labelSettle[i] = { x: l.x + dx, y: l.y + dy };
		}
		const pipeSettle: Record<string, [number, number][]> = {};
		for (const p of doc.pipes ?? []) {
			const pts = groupPipePoints(p, ids, atts, dx, dy);
			if (!pts) continue;
			ops.push({ type: 'setPipePoints', id: p.id, points: pts });
			pipeSettle[p.id] = pts;
		}
		if (!ops.length) return;
		postOp(ops.length > 1 ? { type: 'batch', ops } : ops[0]);
		settle({ eq: Object.fromEntries(eqs.map((q) => [q.id, { x: q.x + dx, y: q.y + dy }])), labels: labelSettle, pipes: pipeSettle });
	}

	function labelDown(e: PointerEvent, i: number, l: { x: number; y: number }) {
		if (ed.tool !== 'select' || e.button !== 0) return;
		e.stopPropagation();
		if (toggleItem(e, { index: i })) {
			groupDown(e, { kind: 'label', index: i }, l.x, l.y, false);
			return;
		}
		if (groupDown(e, { kind: 'label', index: i }, l.x, l.y)) return;
		ed.selection = { kind: 'label', index: i };
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
		const [px, py] = pt(e);
		drag = { kind: 'label', index: i, dx: px - l.x, dy: py - l.y, x: l.x, y: l.y, moved: false };
	}

	// ── pipe selection + vertex handles ─────────────────────────────────────
	function pipeDown(e: PointerEvent, p: MimicPipe) {
		if (ed.tool !== 'select' || e.button !== 0) return;
		e.stopPropagation();
		// BUG 1: Shift is reserved for vtxDown's node multi-select toggle —
		// a shift-click that MISSES a vertex handle (they're small; an easy
		// miss mid a multi-select) and lands on the pipe's own (much wider)
		// stroke used to fall through to here and overwrite the whole
		// in-progress 'nodes' selection with a plain whole-pipe one,
		// silently discarding every point picked so far. No-op instead —
		// same principle as the Shift-click-on-empty-canvas fix below.
		if (e.shiftKey) return;
		ed.selection = { kind: 'pipe', id: p.id };
	}

	/** `i` is a FULL index (pipeHandlePoints() space) — index 0 / the last
	 * index is a terminal handle: an anchor drag (Feature 2's attach/detach)
	 * regardless of whether that end is CURRENTLY anchored (a plain terminal
	 * point can be dragged onto a port to attach it, same gesture). Every
	 * other index is an ordinary interior-point drag, same as always. */
	function vtxDown(e: PointerEvent, p: MimicPipe, i: number) {
		if (e.button !== 0) return;
		e.stopPropagation();
		const full = pipeHandlePoints(p);
		const isStart = i === 0;
		const isEnd = i === full.length - 1;
		if (e.shiftKey && !isStart && !isEnd) {
			// Shift-click builds a node multi-selection on THIS pipe — a
			// separate mode from "whole pipe selected"; starting it (or a
			// plain click elsewhere) drops any other selection. Terminal
			// (anchor-capable) handles don't participate — multi-selecting an
			// endpoint doesn't have a coherent "delete" meaning here.
			const interiorIndex = i - (p.from ? 1 : 0);
			const s = ed.selection;
			const cur = s?.kind === 'nodes' && s.pipeId === p.id ? s.indices : [];
			const indices = cur.includes(interiorIndex)
				? cur.filter((j) => j !== interiorIndex)
				: [...cur, interiorIndex].sort((a, b) => a - b);
			ed.selection = indices.length ? { kind: 'nodes', pipeId: p.id, indices } : null;
			return;
		}
		// BUG 3: a shift-click that MISSES the interior toggle above (isStart
		// or isEnd — the terminal/anchor handle's larger hit radius makes it
		// an easy stray hit while multi-selecting nearby interior points) used
		// to fall straight through to the unconditional selection assignment
		// below, same root cause as BUG 1 (pipeDown/midDown/labelDown/
		// canvasDown) but left uncovered in THIS handler's own terminal
		// branch: it silently clobbered an in-progress 'nodes' multi-selection
		// with a plain 'end' selection — and since a terminal handle's dot
		// never renders a distinct selected style on its own (see the 'end'
		// selection having no visual below), the net effect was every
		// previously-highlighted node's `.sel` dot reverting to its plain
		// look in the same frame: "shift-click a vertex, the dot disappears"
		// with no data mutation (confirmed in the gesture harness). Shift is
		// reserved for the interior toggle above; everywhere else (here, same
		// as every sibling handler) it's a no-op, never a wipe.
		if (e.shiftKey) return;
		// A terminal handle addresses its specific END (kind 'end') rather than
		// the coarser whole-pipe 'pipe' selection every other handle uses — see
		// the Selection type's doc comment: this is what lets the Arrow-key
		// nudge below know WHICH end to detach-and-nudge, and (Feature 2/BUG 2)
		// is the same selection a click on the always-live anchor hit-target
		// (onCanvasCapture, below) produces, so re-clicking an anchored end
		// that's already selected behaves identically either way.
		ed.selection = isStart || isEnd ? { kind: 'end', pipeId: p.id, end: isStart ? 'from' : 'to' } : { kind: 'pipe', id: p.id };
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		if (isStart || isEnd) {
			// Seed `port` with the end's CURRENT anchor (grab starts exactly on
			// it) so the preview doesn't flash unanchored for one frame before
			// the first move re-evaluates the snap.
			const seed = (isStart ? p.from : p.to) ?? null;
			drag = { kind: 'anchor', id: p.id, end: isStart ? 'from' : 'to', x: full[i][0], y: full[i][1], port: seed, moved: false };
			return;
		}
		const interiorIndex = i - (p.from ? 1 : 0);
		drag = { kind: 'vtx', id: p.id, index: interiorIndex, pts: p.points.map((q) => [...q] as [number, number]), moved: false };
	}

	/** Midpoint handle: insert a vertex into the segment and drag it away.
	 * `seg` is a full-index SEGMENT number (between pipeHandlePoints()[seg]
	 * and [seg+1]) — converted to the interior insertion index the same way
	 * vtxDown converts a full vertex index. */
	function midDown(e: PointerEvent, p: MimicPipe, seg: number) {
		if (e.button !== 0) return;
		e.stopPropagation();
		// BUG 1: see pipeDown's identical guard — a shift-click that misses
		// a vertex handle and lands on a midpoint square instead must not
		// clobber an in-progress node multi-selection either.
		if (e.shiftKey) return;
		ed.selection = { kind: 'pipe', id: p.id };
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		const interiorIndex = seg + 1 - (p.from ? 1 : 0);
		const pts = p.points.map((q) => [...q] as [number, number]);
		const [px, py] = pt(e);
		pts.splice(interiorIndex, 0, [snap(px), snap(py)]);
		drag = { kind: 'vtx', id: p.id, index: interiorIndex, pts, moved: true };
	}

	/** Detach an anchored end, materializing it as a concrete point at its
	 * CURRENT derived position — the shared tail of the drag-off gesture
	 * (onup below) and the double-click-an-anchor-dot shortcut (vtxDelete). */
	function detachAnchor(p: MimicPipe, end: 'from' | 'to', point: [number, number]) {
		const pts = p.points.map((q) => [...q] as [number, number]);
		if (end === 'from') pts.unshift(point);
		else pts.push(point);
		postOp({ type: 'updatePipe', id: p.id, patch: { [end]: null, points: pts } });
		settle({ pipes: { [p.id]: pts } });
	}

	/** Arrow-key nudge on a pipe's terminal handle (BUG 2's keyboard-nudge
	 * decision: DETACH-AND-NUDGE, never a silent dead end). An anchored end
	 * has no stored position to nudge — it's derived from the port — so
	 * nudging it detaches first (materializing at its current derived
	 * position, like detachAnchor) and applies the step in the SAME op, one
	 * undo step. A plain (unanchored) terminal point just moves, same as any
	 * other nudge. Either way `ed.selection` stays `{kind:'end', ...}` on
	 * this pipe, so repeated arrow-presses keep nudging it. */
	function nudgeEnd(p: MimicPipe, end: 'from' | 'to', dx: number, dy: number) {
		const full = pipeHandlePoints(p);
		const cur = end === 'from' ? full[0] : full[full.length - 1];
		const next: [number, number] = [cur[0] + dx, cur[1] + dy];
		const pts = p.points.map((q) => [...q] as [number, number]);
		const patch: Record<string, unknown> = {};
		if (end === 'from' ? p.from : p.to) {
			if (end === 'from') pts.unshift(next);
			else pts.push(next);
			patch[end] = null;
		} else if (end === 'from') {
			pts[0] = next;
		} else {
			pts[pts.length - 1] = next;
		}
		patch.points = pts;
		postOp({ type: 'updatePipe', id: p.id, patch });
		settle({ pipes: { [p.id]: pts } });
	}

	/** `i` is a FULL index (see vtxDown). A terminal handle that's anchored
	 * detaches (materializing its current position) rather than deleting —
	 * an anchor reference isn't "a point" to remove, and detach is the
	 * click-alternative to dragging it off. Removing a vertex that leaves
	 * fewer interior points than the pipe's CURRENT anchors allow is no
	 * longer a pipe — same convention as the keyboard multi-node delete
	 * below: cascade to deletePipe rather than silently refusing. */
	function vtxDelete(p: MimicPipe, i: number) {
		const full = pipeHandlePoints(p);
		if (i === 0 && p.from) return detachAnchor(p, 'from', full[i]);
		if (i === full.length - 1 && p.to) return detachAnchor(p, 'to', full[i]);
		const interiorIndex = i - (p.from ? 1 : 0);
		const pts = p.points.filter((_, j) => j !== interiorIndex);
		if (pts.length < minInteriorFloor(p)) {
			postOp({ type: 'deletePipe', id: p.id });
			ed.selection = null;
		} else {
			postOp({ type: 'setPipePoints', id: p.id, points: pts });
			settle({ pipes: { [p.id]: pts } });
		}
	}

	// ── ports editor (equipment connection points) ──────────────────────────
	function portDown(e: PointerEvent, eq: MimicEquipment, i: number) {
		if (e.button !== 0) return;
		e.stopPropagation();
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		portsSelected = i;
		const port = resolvePorts(eq, ed.manifest)[i];
		drag = { kind: 'port', id: eq.id, index: i, fx: port?.x ?? 0.5, fy: port?.y ?? 0.5, part: port?.part, moved: false };
	}

	// ── resize handles (the selected equipment's right / bottom / corner) ──
	const MIN_SIZE = 10;
	function resizeDown(e: PointerEvent, eq: MimicEquipment, edge: 'e' | 's' | 'se') {
		if (e.button !== 0) return;
		e.stopPropagation();
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		// Start from the box as drawn, so a component with no height yet
		// (natural proportions) resizes from exactly what's on screen.
		const b = eqBox(eq);
		const [px, py] = pt(e);
		drag = { kind: 'resize', id: eq.id, edge, px, py, w0: Math.round(b.w), h0: Math.round(b.h), w: Math.round(b.w), h: Math.round(b.h), moved: false };
	}
	/** Smallest canvas an edge drag will make, canvas units. */
	const MIN_CANVAS = 100;
	function canvasResizeDown(e: PointerEvent, edge: 'e' | 's' | 'se') {
		if (e.button !== 0 || !doc) return;
		e.stopPropagation();
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		// Hold the current scale: in fit mode the canvas would otherwise
		// rescale to the pane on drop and jump out from under the pointer.
		// Fit (Ctrl+0) hands back to the pane.
		zoom = scale;
		// From the size as drawn, so a drag that follows one the doc hasn't
		// echoed yet starts where the canvas is, not where it was.
		const [px, py] = pt(e);
		const { width: w, height: h } = canvasSize;
		drag = { kind: 'canvas', edge, px, py, w0: w, h0: h, w, h, moved: false };
	}
	const resizable = (eq: MimicEquipment): boolean =>
		ed.tool === 'select' && !ed.portsEdit && ed.selection?.kind === 'equipment' && ed.selection.id === eq.id;

	/** Double-click on the equipment box itself (ports-edit mode) adds a
	 * port; double-clicking an existing dot (see the port handle markup)
	 * removes it instead and stops this from also firing. */
	function eqDblclick(e: MouseEvent, eq: MimicEquipment) {
		if (ed.portsEdit?.id !== eq.id) return;
		e.stopPropagation();
		addPort(eq, pt(e));
	}

	// ── window-level move/up (pointer capture bubbles here) ─────────────────
	function onmove(e: PointerEvent) {
		if (canvasEl && (ed.tool === 'pipe' || ed.tool === 'place' || ed.tool === 'label')) {
			cursor = ed.tool === 'pipe' ? nextPoint(pt(e), e.shiftKey) : [snap(pt(e)[0]), snap(pt(e)[1])];
			hoverPort = ed.tool === 'pipe' ? portSnapNamed(pt(e)) : null;
		}
		if (!drag) return;
		if (drag.kind === 'eq' || drag.kind === 'label') {
			const [px, py] = pt(e);
			const nx = snap(px - drag.dx);
			const ny = snap(py - drag.dy);
			if (nx !== drag.x || ny !== drag.y) {
				drag.x = nx;
				drag.y = ny;
				drag.moved = true;
			}
		} else if (drag.kind === 'box') {
			[drag.x1, drag.y1] = pt(e);
			// A few screen pixels of slop, so a shift-click stays a click.
			if (!drag.moved && Math.hypot(drag.x1 - drag.x0, drag.y1 - drag.y0) * scale < 4) return;
			drag.moved = true;
			const hits = boxHits(drag);
			if (!hits.ids.length && !hits.labels.length) ed.selection = drag.orig;
			else
				selectItems(
					[...new Set([...drag.base.ids, ...hits.ids])],
					[...new Set([...drag.base.labels, ...hits.labels])]
				);
		} else if (drag.kind === 'group') {
			const [px, py] = pt(e);
			const dx = snap(px - drag.gx) - drag.ax;
			const dy = snap(py - drag.gy) - drag.ay;
			if (dx !== drag.dx || dy !== drag.dy) {
				drag.dx = dx;
				drag.dy = dy;
				drag.moved = true;
			}
		} else if (drag.kind === 'resize' || drag.kind === 'canvas') {
			const [px, py] = pt(e);
			const min = drag.kind === 'canvas' ? MIN_CANVAS : MIN_SIZE;
			const w = drag.edge === 's' ? drag.w0 : Math.max(min, snap(drag.w0 + px - drag.px));
			const h = drag.edge === 'e' ? drag.h0 : Math.max(min, snap(drag.h0 + py - drag.py));
			if (w !== drag.w || h !== drag.h) {
				drag.w = w;
				drag.h = h;
				drag.moved = true;
			}
		} else if (drag.kind === 'port') {
			const id = drag.id;
			const eq = (doc?.equipment ?? []).find((e) => e.id === id);
			if (eq) {
				// Over a part of the drawing the port anchors to it; off every
				// part it's a fraction of the box.
				const to = dropPort(eqBox(eq), partsOf(eq), pt(e));
				if (to.x !== drag.fx || to.y !== drag.fy || to.part !== drag.part) {
					drag.fx = to.x;
					drag.fy = to.y;
					drag.part = to.part;
					drag.moved = true;
				}
			}
		} else if (drag.kind === 'anchor') {
			const raw = pt(e);
			const named = portSnapNamed(raw);
			const np = named ? [named.x, named.y] : [snap(raw[0]), snap(raw[1])];
			const nextPort: MimicPipeAnchor | null = named ? { equip: named.equip, port: named.port } : null;
			const portChanged = (drag.port?.equip ?? '') !== (nextPort?.equip ?? '') || (drag.port?.port ?? '') !== (nextPort?.port ?? '');
			if (np[0] !== drag.x || np[1] !== drag.y || portChanged) {
				drag.x = np[0];
				drag.y = np[1];
				drag.port = nextPort;
				drag.moved = true;
			}
		} else {
			const raw = pt(e);
			const np = portSnap(raw) ?? [snap(raw[0]), snap(raw[1])];
			const cur = drag.pts[drag.index];
			if (np[0] !== cur[0] || np[1] !== cur[1]) {
				drag.pts[drag.index] = np;
				drag.moved = true;
			}
		}
	}

	/** Attach/detach on drop (Feature 2): re-checks the dropped position
	 * against the ports — landing on one attaches THAT end to it (removing
	 * the corresponding interior point if this end wasn't already anchored:
	 * it's now represented by the anchor, not stored); landing off any port
	 * detaches (or, for an already-plain end, is just an ordinary move) by
	 * writing the dropped position as a concrete point instead. Either way
	 * the interior points array is patched in the SAME op as the anchor
	 * change so the pipe never transiently violates its point-count floor. */
	function dropAnchor(p: MimicPipe, end: 'from' | 'to', x: number, y: number) {
		const wasAnchored = end === 'from' ? !!p.from : !!p.to;
		const named = portSnapNamed([x, y]);
		const pts = p.points.map((q) => [...q] as [number, number]);
		const patch: Record<string, unknown> = {};
		if (named) {
			if (!wasAnchored) {
				if (end === 'from') pts.shift();
				else pts.pop();
			}
			patch.points = pts;
			patch[end] = { equip: named.equip, port: named.port } satisfies MimicPipeAnchor;
		} else {
			if (wasAnchored) {
				if (end === 'from') pts.unshift([x, y]);
				else pts.push([x, y]);
				patch[end] = null;
			} else if (end === 'from') {
				pts[0] = [x, y];
			} else {
				pts[pts.length - 1] = [x, y];
			}
			patch.points = pts;
		}
		postOp({ type: 'updatePipe', id: p.id, patch });
		settle({ pipes: { [p.id]: pts } });
	}

	function onup() {
		if (!drag) return;
		if (drag.kind === 'box') {
			drag = null;
			return;
		}
		if (drag.kind === 'group') {
			if (drag.moved) moveGroup(drag.ids, drag.labels, drag.dx, drag.dy, drag.atts);
			else if (drag.hit) ed.selection = drag.hit;
			drag = null;
			return;
		}
		if (drag.moved) {
			if (drag.kind === 'eq') {
				const eq = (doc?.equipment ?? []).find((e) => e.id === (drag as { id: string }).id);
				if (eq) postEqMove(eq, drag.atts, drag.x, drag.y);
			} else if (drag.kind === 'resize') {
				postOp({ type: 'updateEquipment', id: drag.id, patch: { width: drag.w, height: drag.h } });
				settle({ sizes: { [drag.id]: { width: drag.w, height: drag.h } } });
			} else if (drag.kind === 'canvas') {
				postOp({ type: 'setCanvas', width: drag.w, height: drag.h });
				settle({ canvas: { width: drag.w, height: drag.h } });
			} else if (drag.kind === 'label') {
				postOp({ type: 'updateLabel', index: drag.index, patch: { x: drag.x, y: drag.y } });
				settle({ labels: { [drag.index]: { x: drag.x, y: drag.y } } });
			} else if (drag.kind === 'port') {
				const id = drag.id;
				const eq = (doc?.equipment ?? []).find((e) => e.id === id);
				if (eq) {
					const ports = resolvePorts(eq, ed.manifest).map((p) => ({ ...p }));
					ports[drag.index] = movePort(ports[drag.index], { x: drag.fx, y: drag.fy, part: drag.part });
					commitPorts(eq, ports);
				}
			} else if (drag.kind === 'anchor') {
				const id = drag.id;
				const p = (doc?.pipes ?? []).find((pp) => pp.id === id);
				if (p) dropAnchor(p, drag.end, drag.x, drag.y);
			} else {
				const pts = $state.snapshot(drag.pts) as [number, number][];
				postOp({ type: 'setPipePoints', id: drag.id, points: pts });
				settle({ pipes: { [drag.id]: pts } });
			}
		}
		drag = null;
	}

	// ── BUG 2: anchored pipe-end hit priority over equipment ────────────────
	// An anchored pipe end renders exactly ON its equipment's port dot, which
	// sits ON that equipment's own box — and equipment (.eq, further down in
	// the DOM/paint order) beats the pipe SVG on any overlapping pixel, so
	// clicking the anchor selected the equipment underneath instead (BUG 2's
	// actual cause). A vertex handle rendered only for the ALREADY-selected
	// pipe (pipeHandlesVisible) can't fix the FIRST click anyway — nothing to
	// click on yet. Fixed by intercepting at the CAPTURE phase on the canvas
	// root — capture runs root-to-target, fully before .eq's own (bubble
	// phase) pointerdown listener ever fires — and doing the same proximity
	// math portSnapNamed() uses for placing a NEW anchor, rather than relying
	// on DOM paint order at all. This wins for every anchored end regardless
	// of current selection (so both "click to select" and "click to re-grab
	// an already-selected anchor" work), while leaving an ordinary click on
	// equipment (nothing anchored nearby) completely alone.
	const ANCHOR_HIT = 10;
	function onCanvasCapture(e: PointerEvent) {
		if (ed.tool !== 'select' || e.button !== 0 || !doc || ed.portsEdit) return;
		const p = pt(e);
		for (const pipe of doc.pipes ?? []) {
			if (!pipe.from && !pipe.to) continue;
			const full = pipeHandlePoints(pipe);
			if (pipe.from && Math.hypot(full[0][0] - p[0], full[0][1] - p[1]) <= ANCHOR_HIT) {
				vtxDown(e, pipe, 0);
				return;
			}
			const last = full.length - 1;
			if (pipe.to && Math.hypot(full[last][0] - p[0], full[last][1] - p[1]) <= ANCHOR_HIT) {
				vtxDown(e, pipe, last);
				return;
			}
		}
	}

	// ── canvas clicks: placement, labels, pipe points, deselect ─────────────
	function nextPoint(p: [number, number], free: boolean): [number, number] {
		// A nearby connection point wins outright — connecting is the intent.
		const port = portSnap(p);
		if (port) return port;
		const s: [number, number] = [snap(p[0]), snap(p[1])];
		const prev = draft.at(-1);
		if (!prev || free) return s;
		// Orthogonal by default: land on the dominant axis from the last point.
		return Math.abs(s[0] - prev[0]) >= Math.abs(s[1] - prev[1]) ? [s[0], prev[1]] : [prev[0], s[1]];
	}

	function boxDown(e: PointerEvent, [x, y]: [number, number]) {
		if (ed.portsEdit) return;
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		drag = { kind: 'box', x0: x, y0: y, x1: x, y1: y, base: { ids: selEquipIds(), labels: selLabels() }, orig: ed.selection, moved: false };
	}
	/** Equipment and labels lying wholly inside the box. */
	function boxHits(b: { x0: number; y0: number; x1: number; y1: number }): { ids: string[]; labels: number[] } {
		const [l, r] = [Math.min(b.x0, b.x1), Math.max(b.x0, b.x1)];
		const [t, btm] = [Math.min(b.y0, b.y1), Math.max(b.y0, b.y1)];
		const inside = (q: { x: number; y: number; w: number; h: number }) => q.x >= l && q.y >= t && q.x + q.w <= r && q.y + q.h <= btm;
		const ids = (doc?.equipment ?? []).filter((q) => inside(eqBox(q))).map((q) => q.id);
		const labels: number[] = [];
		canvasEl?.querySelectorAll<HTMLElement>('.lbl[data-id]').forEach((el) => {
			if (inside({ x: el.offsetLeft, y: el.offsetTop, w: el.offsetWidth, h: el.offsetHeight })) labels.push(Number(el.dataset.id));
		});
		return { ids, labels };
	}

	function canvasDown(e: PointerEvent) {
		if (e.button !== 0) return;
		const p = pt(e);
		if (ed.tool === 'place' && ed.placeComponent) {
			postOp({ type: 'addEquipment', component: ed.placeComponent, x: snap(p[0]), y: snap(p[1]) });
			ed.tool = 'select';
			ed.placeComponent = '';
			return;
		}
		if (ed.tool === 'label') {
			postOp({ type: 'addLabel', text: 'text', x: snap(p[0]), y: snap(p[1]) });
			ed.tool = 'select';
			return;
		}
		if (ed.tool === 'pipe') {
			const np = nextPoint(p, e.shiftKey);
			const last = draft.at(-1);
			if (!last || np[0] !== last[0] || np[1] !== last[1]) {
				// Named at the RAW click point (not the possibly axis-adjusted
				// `np`) — nextPoint() already made port-snapping win outright
				// over the orthogonal-axis snap, so when np came from a port
				// this agrees; checking the raw point directly keeps this
				// independent of that precedence detail.
				const named = portSnapNamed(p);
				const anchor: MimicPipeAnchor | null = named ? { equip: named.equip, port: named.port } : null;
				if (draft.length === 0) draftFrom = anchor;
				draftLastSnap = anchor;
				draft.push(np);
			}
			return;
		}
		// BUG 1 root cause (confirmed in the gesture harness): this fallback
		// only runs in 'select' mode (every other tool branch above returns
		// first) — an ordinary click on empty canvas deselecting is correct
		// and intended. But a SHIFT-click that merely MISSES its target
		// (the vtx handles it's meant for are small; an easy miss while
		// multi-selecting several) landed here too and unconditionally
		// nulled `ed.selection`, silently discarding an entire in-progress
		// node multi-selection with no visual feedback — so by the time the
		// user pressed Delete there was nothing left selected to delete.
		// Shift is reserved for vtxDown's node-toggle gesture; everywhere
		// else (here, pipeDown, midDown) it's a no-op, never a wipe. A
		// Shift-DRAG here draws a selection box (boxDown); released without
		// a move it is still that no-op.
		if (e.shiftKey) {
			boxDown(e, p);
			return;
		}
		ed.selection = null;
	}

	/** The pipe the current draw draft WOULD commit — the single source of
	 * truth shared by the live preview (the rubber-band path below) and
	 * finishPipe(), so what you see rubber-banding is exactly what Enter /
	 * double-click creates (no parallel preview math). Null until the draft is
	 * a completable pipe.
	 *
	 * resolveDraftFinish() (pipeDraft.ts) decides the point set + anchors (and
	 * whether the live rubber-band end counts — see its doc comment: a hovered
	 * destination port is included even once the clicked draft already meets
	 * the floor, which is the "Enter completes but not TO the port" fix). The
	 * ROUTE SUGGESTION here then mirrors what a both-ends-anchored/zero-interior
	 * draw becomes: an orthogonal auto-route (suggestRoute) instead of a bare
	 * diagonal — the port-to-port gesture the router exists for. Feeding the
	 * result through routedPoints() below renders the SAME stubs/corners the
	 * committed pipe will. */
	const draftSpec = $derived.by((): { points: [number, number][]; from?: MimicPipeAnchor; to?: MimicPipeAnchor; routing?: 'orthogonal' } | null => {
		const snapDraft = $state.snapshot(draft) as [number, number][];
		const result = resolveDraftFinish(snapDraft, draftFrom, draftLastSnap, cursor, hoverPort);
		if (!result) return null;
		let points = result.points;
		let routing: 'orthogonal' | undefined;
		if (result.from && result.to && points.length === 0) {
			const startPort = getPort(result.from.equip, result.from.port);
			const endPort = getPort(result.to.equip, result.to.port);
			if (startPort && endPort) {
				points = suggestRoute(startPort, startPort.dir, endPort, endPort.dir, obstaclesFor(), {
					start: keepOutOf(result.from.equip),
					end: keepOutOf(result.to.equip)
				});
				routing = 'orthogonal';
			}
		}
		return {
			points,
			...(result.from ? { from: result.from } : {}),
			...(result.to ? { to: result.to } : {}),
			...(routing ? { routing } : {})
		};
	});

	/** Drop the pipe-drawing draft (points + anchors) — Esc, a finish (whether
	 * or not its op got through), or leaving the + Pipe tool. */
	function cancelDraft() {
		draft = [];
		draftFrom = null;
		draftLastSnap = null;
	}

	/** Finish the pipe-drawing draft (Enter or double-click): commit whatever
	 * draftSpec currently represents — identical to what the preview shows.
	 * The draft is dropped however this ends: a finish that couldn't commit
	 * (postOp reports — and toasts — a failed post) must not leave a dashed
	 * draft stranded on the canvas. The anchors are `$state` proxies; postOp
	 * copies the op to plain data before it crosses postMessage. */
	function finishPipe() {
		try {
			const spec = draftSpec;
			if (spec) {
				const sent = postOp({
					type: 'addPipe',
					points: spec.points,
					...(spec.from ? { from: spec.from } : {}),
					...(spec.to ? { to: spec.to } : {}),
					...(spec.routing ? { routing: spec.routing } : {})
				});
				if (sent) ed.tool = 'select';
			}
		} finally {
			cancelDraft();
		}
	}

	// A draft belongs to the + Pipe tool: switching tools (the toolbar, a
	// palette pick) drops it, so it can't linger on the Select canvas.
	$effect(() => {
		if (ed.tool !== 'pipe' && draft.length) cancelDraft();
	});

	// ── keyboard ────────────────────────────────────────────────────────────
	function onkeydown(e: KeyboardEvent) {
		// Stale canvas while the JSON doesn't parse (MimicApp locks it):
		// no keyboard gestures either.
		if (ed.error) return;
		if (typingTarget(e)) return;
		if ((e.ctrlKey || e.metaKey) && !e.altKey && ed.tool === 'select' && !ed.portsEdit) {
			const k = e.key.toLowerCase();
			let acted = false;
			if (k === 'c') acted = copySel();
			else if (k === 'x') acted = cutSel();
			else if (k === 'd') acted = duplicateSel();
			else if (k === 'v') {
				void pasteFromClipboard();
				acted = true;
			} else if (k === 'a') {
				selectEquip((doc?.equipment ?? []).map((q) => q.id));
				acted = true;
			}
			if (acted) {
				e.preventDefault();
				return;
			}
		}
		if (e.key === 'Escape') {
			if (draft.length) {
				cancelDraft();
			} else if (ed.tool !== 'select') {
				ed.tool = 'select';
				ed.placeComponent = '';
			} else if (ed.portsEdit) ed.portsEdit = null;
			else ed.selection = null;
			return;
		}
		if (e.key === 'Enter' && ed.tool === 'pipe') {
			finishPipe();
			return;
		}
		if (e.key.toLowerCase() === 'p' && ed.tool === 'select' && ed.selection?.kind === 'equipment') {
			const id = ed.selection.id;
			ed.portsEdit = ed.portsEdit?.id === id ? null : { id, target: ed.portsEdit?.target ?? 'manifest' };
			e.preventDefault();
			return;
		}
		if ((e.key === 'Delete' || e.key === 'Backspace') && ed.selection) {
			const s = ed.selection;
			ed.selection = null;
			if (s.kind === 'equipment') deleteEquipmentWithAnchors(s.id);
			else if (s.kind === 'multi') deleteEquipmentWithAnchors(s.ids, s.labels ?? []);
			else if (s.kind === 'pipe') postOp({ type: 'deletePipe', id: s.id });
			else if (s.kind === 'end') postOp({ type: 'deletePipe', id: s.pipeId });
			else if (s.kind === 'nodes') {
				const p = (doc?.pipes ?? []).find((p) => p.id === s.pipeId);
				if (p) {
					const pts = p.points.filter((_, i) => !s.indices.includes(i));
					// Fewer interior points left than the pipe's anchors allow —
					// no longer a pipe (or, unanchored, below the classic floor
					// of 2) — drop it.
					if (pts.length < minInteriorFloor(p)) postOp({ type: 'deletePipe', id: s.pipeId });
					else {
						postOp({ type: 'setPipePoints', id: s.pipeId, points: pts });
						// Same anti-flicker holdover every other pipe-mutating
						// gesture uses (vtx drag, vtxDelete, eq move): render the
						// post-delete shape immediately instead of the pipe's
						// stale (pre-op) points for the round-trip's duration.
						settle({ pipes: { [s.pipeId]: pts } });
					}
				}
			} else postOp({ type: 'deleteLabel', index: s.index });
			return;
		}
		// Ports-edit mode owns arrow keys outright: nudge the selected port
		// (panel row or dot — either sets portsSelected) and NEVER fall through
		// to the equipment-nudge branch below, even when no port is selected —
		// that fallthrough (arrows moving the whole instance while the user is
		// clearly editing its ports) is the bug this branch exists to prevent.
		if (e.key.startsWith('Arrow') && ed.portsEdit) {
			e.preventDefault();
			const eq = (doc?.equipment ?? []).find((q) => q.id === ed.portsEdit!.id);
			if (portsSelected === null || !eq) return;
			const step = e.shiftKey ? GRID : 1;
			const dx = e.key === 'ArrowLeft' ? -step : e.key === 'ArrowRight' ? step : 0;
			const dy = e.key === 'ArrowUp' ? -step : e.key === 'ArrowDown' ? step : 0;
			if (!dx && !dy) return;
			const ports = resolvePorts(eq, ed.manifest).map((p) => ({ ...p }));
			ports[portsSelected] = nudgePort(eqBox(eq), ports[portsSelected], dx, dy);
			commitPorts(eq, ports);
			return;
		}
		if (e.key.startsWith('Arrow') && ed.selection && doc) {
			const step = e.shiftKey ? GRID : 1;
			const dx = e.key === 'ArrowLeft' ? -step : e.key === 'ArrowRight' ? step : 0;
			const dy = e.key === 'ArrowUp' ? -step : e.key === 'ArrowDown' ? step : 0;
			if (!dx && !dy) return;
			const s = ed.selection;
			if (s.kind === 'equipment') {
				const eq = (doc.equipment ?? []).find((q) => q.id === s.id);
				if (eq) postEqMove(eq, attachmentsFor(eq), eq.x + dx, eq.y + dy);
			} else if (s.kind === 'multi') {
				moveGroup(s.ids, s.labels ?? [], dx, dy);
			} else if (s.kind === 'label') {
				const l = (doc.labels ?? [])[s.index];
				if (l) {
					postOp({ type: 'updateLabel', index: s.index, patch: { x: l.x + dx, y: l.y + dy } });
					settle({ labels: { [s.index]: { x: l.x + dx, y: l.y + dy } } });
				}
			} else if (s.kind === 'end') {
				const p = (doc.pipes ?? []).find((pp) => pp.id === s.pipeId);
				if (p) nudgeEnd(p, s.end, dx, dy);
			}
			e.preventDefault();
		}
	}

	const handleR = $derived(5 / scale);
	// Port dots show wherever a pipe end or vertex can land on one: while
	// drawing (+ Pipe), and while dragging a pipe's vertex or END in Select.
	const showPorts = $derived(ed.tool === 'pipe' || drag?.kind === 'vtx' || drag?.kind === 'anchor');
	/** The port a live gesture would land on — the drawing cursor's, or a
	 * dragged pipe end's snap — highlighted among the dots. */
	const litPort = $derived(
		drag?.kind === 'anchor' ? (drag.port ?? null) : hoverPort ? { equip: hoverPort.equip, port: hoverPort.port } : null
	);
	/** `kind: 'end'` (a pipe's terminal handle selected) counts as its pipe
	 * being 'pipe'-selected for every visual/handles purpose below — it's
	 * only a distinct Selection variant so keyboard nudge can address the
	 * specific end (see the type's doc comment); nothing else needs to tell
	 * the two apart. */
	const selected = (kind: 'equipment' | 'pipe' | 'label', key: string | number): boolean => {
		const s = ed.selection;
		if (!s) return false;
		if (kind === 'pipe' && s.kind === 'end') return s.pipeId === key;
		if (kind === 'equipment' && s.kind === 'multi') return s.ids.includes(key as string);
		if (kind === 'label' && s.kind === 'multi') return (s.labels ?? []).includes(key as number);
		if (s.kind !== kind) return false;
		return 'id' in s ? s.id === key : s.index === key;
	};
	/** Vertex/midpoint handles show for the whole-pipe-selected pipe AND for
	 * whichever pipe currently holds a node multi-selection or an 'end'
	 * (terminal-handle) selection. */
	const pipeHandlesVisible = (id: string): boolean => {
		const s = ed.selection;
		if (!s) return false;
		if (s.kind === 'pipe') return s.id === id;
		if (s.kind === 'nodes' || s.kind === 'end') return s.pipeId === id;
		return false;
	};
	const nodeSelected = (pipeId: string, i: number): boolean => {
		const s = ed.selection;
		return !!s && s.kind === 'nodes' && s.pipeId === pipeId && s.indices.includes(i);
	};
	/** BUG 3's other half: a pipe's OWN terminal handle (anchor ring or plain
	 * point) never had a `nodeSelected`-style check of its own — `kind:'end'`
	 * is a DIFFERENT Selection variant (see its doc comment), so the dot for
	 * the specific end `ed.selection` addresses rendered identically whether
	 * selected or not. Mirrors `nodeSelected` for the `end` variant so both
	 * the anchor ring (`.vtx.anchor.sel`) and a plain (unanchored) terminal
	 * point (`.vtx.sel`, same rule interior vertices use) actually show it. */
	const endSelected = (pipeId: string, end: 'from' | 'to'): boolean => {
		const s = ed.selection;
		return !!s && s.kind === 'end' && s.pipeId === pipeId && s.end === end;
	};

	// ── ports-edit side panel (PortsPanel) ───────────────────────────────────
	const portsEditEq = $derived(
		ed.portsEdit ? ((doc?.equipment ?? []).find((e) => e.id === ed.portsEdit!.id) ?? null) : null
	);
	$effect(() => {
		void ed.portsEdit;
		portsSelected = null;
	});
	function renamePortEC(eq: MimicEquipment, i: number, name: string): boolean {
		const trimmed = name.trim();
		const existing = resolvePorts(eq, ed.manifest);
		if (trimmed === '' || existing.some((p, j) => j !== i && p.name === trimmed)) return false;
		commitPorts(eq, existing.map((p, j) => (j === i ? { ...p, name: trimmed } : p)));
		return true;
	}
	function setPortDirEC(eq: MimicEquipment, i: number, dir: PortDir | null) {
		const existing = resolvePorts(eq, ed.manifest);
		commitPorts(eq, existing.map((p, j) => (j === i ? { ...p, dir: dir ?? undefined } : p)));
	}
	function addPortAtFreeSlotEC(eq: MimicEquipment) {
		const existing = resolvePorts(eq, ed.manifest);
		commitPorts(eq, [...existing, newPortAtFreeSlot(existing)]);
	}
</script>

<svelte:window onpointermove={onmove} onpointerup={onup} {onkeydown} />
<svelte:document onkeydown={onZoomKey} />

<div class="cpane">
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="wrap" bind:this={wrap} onpointerdown={panDown} onpointermove={panMove} onpointerup={panUp} onpointercancel={panUp}>
	{#if doc}
		<!-- The scaled canvas's footprint: a transform doesn't change layout
		     size, so this box gives the pane its scroll extent at any zoom
		     (plus room for handles that sit just past the canvas edge). -->
		<div class="sizer" class:sizing={drag?.kind === 'canvas'} style="width: {cw * scale + 12}px; height: {ch * scale + 12}px">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="canvas"
			class:crosshair={ed.tool !== 'select'}
			data-theme={ed.previewTheme ?? themeColorMode.mode}
			bind:this={canvasEl}
			style="width: {cw}px; height: {ch}px; transform: scale({scale}); --grid-px: {GRID}px"
			onpointerdowncapture={onCanvasCapture}
			onpointerdown={canvasDown}
			ondblclick={() => ed.tool === 'pipe' && finishPipe()}
		>
			<svg
				class="pipes"
				width={cw}
				height={ch}
				viewBox="0 0 {cw} {ch}"
			>
				<!-- Layered like the kit's Mimic: every wall, then every bore,
				     then every flow overlay, so runs UNION at junctions instead
				     of a later pipe's background-coloured bore punching a slot
				     across an earlier pipe's wall (see the junction note in the
				     kit's Pipe.svelte) — the editor must show the tee the
				     runtime will draw. Selection halo rides the wall pass
				     (under everything of its own pipe and every bore); the hit
				     strokes come last so they win pointer events over all
				     paint. -->
				{#each PIPE_LAYERS as layer (layer)}
					{#each doc.pipes ?? [] as p (p.id)}
						{@const d = pointsToPath(routedPoints(pipeRouteInput(p), getPort))}
						<g class="piperun" class:sel={selected('pipe', p.id)} class:flagged={pipeFlagged(p)} data-id={p.id} data-kind="pipe">
							{#if layer === 'wall' && selected('pipe', p.id)}
								<path class="pipesel" {d} style="stroke-width: {10 / scale}" />
							{/if}
							<Pipe
								{d}
								{...(p.color !== undefined ? { color: p.color } : {})}
								{...(p.props ?? {})}
								{...resolveBindings(p.bind, ed.tags ?? {})}
								{layer}
							/>
						</g>
					{/each}
				{/each}
				{#each doc.pipes ?? [] as p (p.id)}
					{@const d = pointsToPath(routedPoints(pipeRouteInput(p), getPort))}
					<g class="piperun" class:flagged={pipeFlagged(p)}>
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<path class="hit" {d} data-id={p.id} data-kind="pipe" style="stroke-width: {14 / scale}" onpointerdown={(e) => pipeDown(e, p)} />
					</g>
				{/each}

				{#if showPorts}
					{#each doc.equipment ?? [] as eq (eq.id)}
						{#each eqPorts(eq) as port (port.name)}
							{@const isHover = litPort && litPort.equip === eq.id && litPort.port === port.name}
							{#if port.dir}
								{@const [tx, ty] = tickEnd(port)}
								<line class="porttick" x1={port.x} y1={port.y} x2={tx} y2={ty} style="stroke-width: {1.5 / scale}" />
							{/if}
							<circle class="port" class:hover={isHover} data-id={port.name} data-kind="port" data-equip={eq.id} cx={port.x} cy={port.y} r={(isHover ? 6 : 4) / scale}
								><title>{port.name} · {fmtFraction(port.fx, port.fy)}{port.dir ? ` · exits ${port.dir}` : ''}</title></circle
							>
						{/each}
					{/each}
				{/if}

				{#if draft.length}
					<!-- The SOLID preview is the committed shape (draftSpec routed
					     through the same routedPoints() the finished pipe uses —
					     stubs, orthogonal corners and all), so what rubber-bands is
					     what Enter creates. Until the draft is completable, fall
					     back to the raw click polyline. -->
					{@const previewPts = draftSpec
						? routedPoints(draftSpec, getPort)
						: cursor
							? ([...draft, cursor] as [number, number][])
							: draft}
					<path class="draft" d={pointsToPath(previewPts as [number, number][])} style="stroke-width: {2 / scale}" />
					<!-- Provisional next-segment: when the committed shape doesn't
					     already reach the cursor (an ordinary point drifting over
					     empty canvas that Enter would NOT commit), still show where
					     the next click would land. -->
					{#if draftSpec && cursor}
						{@const end = previewPts[previewPts.length - 1]}
						{#if end && (end[0] !== cursor[0] || end[1] !== cursor[1])}
							<path
								class="draft pending"
								d={pointsToPath([end, cursor] as [number, number][])}
								style="stroke-width: {2 / scale}"
							/>
						{/if}
					{/if}
					{#each draft as [x, y], i (i)}
						<circle
							class="draftpt"
							class:anchor={(i === 0 && draftFrom) || (i === draft.length - 1 && draftLastSnap)}
							cx={x}
							cy={y}
							r={3 / scale}
						/>
					{/each}
				{/if}

			</svg>

			{#each doc.equipment ?? [] as eq (eq.id)}
				{@const C = registry[eq.component]}
				{@const pos = dispEq(eq)}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div
					class="eq"
					data-id={eq.id}
					data-kind="equipment"
					class:sel={selected('equipment', eq.id)}
					class:dragging={drag?.kind === 'eq' && drag.id === eq.id}
					class:portsedit={ed.portsEdit?.id === eq.id}
					class:behind={eq.behind}
					style="left: {pos.x}px; top: {pos.y}px"
					use:regEl={eq.id}
					onpointerdown={(e) => eqDown(e, eq)}
					ondblclick={(e) => eqDblclick(e, eq)}
				>
					{#if C}<C {...eqProps(eq)} />{:else}<UserIsland name={eq.component} props={eqProps(eq)} />{/if}
					{#if resizable(eq)}
						{#each ['e', 's', 'se'] as const as edge (edge)}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<div
								class="rs rs-{edge}"
								style="--hs: {8 / scale}px"
								title="drag to resize"
								onpointerdown={(e) => resizeDown(e, eq, edge)}
							></div>
						{/each}
					{/if}
				</div>
			{/each}

			<!-- BUG 2: a selected pipe's vertex/midpoint handles, in their OWN
			     svg layer placed AFTER the equipment boxes above — so an
			     anchored terminal handle (rendered exactly on its equipment's
			     port, on top of that equipment's own box) paints ABOVE it and
			     is both visible and clickable there, rather than being
			     underneath it in z-order (the equipment box, later in the
			     first "pipes" svg's paint order, used to win every hit-test on
			     that pixel — the root cause of "clicking an anchored end
			     selects the equipment instead"). The very FIRST click on an
			     anchored end (before its pipe is selected, so this layer isn't
			     showing yet) is covered separately by onCanvasCapture's
			     proximity check, above. -->
			<svg
				class="pipe-handles"
				width={cw}
				height={ch}
				viewBox="0 0 {cw} {ch}"
			>
				{#each doc.pipes ?? [] as p (p.id)}
					{#if pipeHandlesVisible(p.id)}
						{@const pts = pipeHandlePoints(p)}
						{#each pts.slice(0, -1) as [x1, y1], i (i)}
							{@const [x2, y2] = pts[i + 1]}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<rect
								class="mid"
								data-id={p.id}
								data-kind="segment"
								x={(x1 + x2) / 2 - handleR * 0.7}
								y={(y1 + y2) / 2 - handleR * 0.7}
								width={handleR * 1.4}
								height={handleR * 1.4}
								onpointerdown={(e) => midDown(e, p, i)}
							/>
						{/each}
						{#each pts as [x, y], i (i)}
							{@const isAnchorEnd = (i === 0 && !!p.from) || (i === pts.length - 1 && !!p.to)}
							{@const interiorIdx = i - (p.from ? 1 : 0)}
							{@const isTerminal = i === 0 || i === pts.length - 1}
							{@const endKey = i === 0 ? 'from' : 'to'}
							{#if isAnchorEnd}
								<!-- A ring-plus-center-dot reads as "anchor" (a pinned
								     reference) rather than "a point" — distinct from
								     both the plain .vtx below and the equipment's own
								     .port dot. class:sel (BUG 3 sweep): a `kind:'end'`
								     selection targeting THIS end used to render with no
								     visual at all — nodeSelected only recognizes a
								     'nodes' multi-selection, a different Selection
								     variant entirely (see endSelected's doc comment). -->
								<!-- svelte-ignore a11y_no_static_element_interactions -->
								<circle
									class="vtx anchor"
									data-id={p.id}
									data-kind="vertex"
									data-end={endKey}
									class:sel={endSelected(p.id, endKey)}
									cx={x}
									cy={y}
									r={handleR * 1.5}
									onpointerdown={(e) => vtxDown(e, p, i)}
									ondblclick={(e) => {
										e.stopPropagation();
										// BUG 1 hardening: a Shift-held double-click reaches here
										// too (two rapid shift-clicks on the SAME handle, e.g.
										// re-clicking one to confirm it's picked, hit this in
										// testing when Chrome's own click-merge timing happened
										// to coalesce the pair). vtxDelete mutates the pipe's
										// points UNCONDITIONALLY — it must never run when the
										// gesture is a shift toggle (vtxDown, above, already owns
										// that): Shift means "multi-select", never "delete this
										// point outright".
										if (e.shiftKey) return;
										vtxDelete(p, i);
									}}
								/>
								<circle class="anchordot" class:sel={endSelected(p.id, endKey)} cx={x} cy={y} r={handleR * 0.5} />
							{:else}
								<!-- svelte-ignore a11y_no_static_element_interactions -->
								<circle
									class="vtx"
									data-id={p.id}
									data-kind="vertex"
									class:sel={isTerminal ? endSelected(p.id, endKey) : nodeSelected(p.id, interiorIdx)}
									cx={x}
									cy={y}
									r={handleR}
									onpointerdown={(e) => vtxDown(e, p, i)}
									ondblclick={(e) => {
										e.stopPropagation();
										// BUG 1 — see the anchor handle's identical guard above.
										if (e.shiftKey) return;
										vtxDelete(p, i);
									}}
								/>
							{/if}
						{/each}
					{/if}
				{/each}
			</svg>

			<!-- PORT-DOT Z-ORDER (BUG 2's pattern, applied here too): ports-edit
			     dots sit ON their equipment's edge, same as an anchored pipe
			     end sits on its port — and used to render inside the FIRST
			     "pipes" svg, painted BEFORE (so losing every hit-test to) the
			     `.eq` boxes above. A dead-center click landed on the box
			     instead of the dot. Fixed the same way pipe-handles was: its
			     own svg layer painted AFTER the equipment boxes, so the dot
			     wins the z-order outright rather than needing a proximity
			     capture-phase hack. -->
			{#if ed.portsEdit}
				{@const peq = (doc.equipment ?? []).find((e) => e.id === ed.portsEdit?.id)}
				{#if peq}
					<svg
						class="portedit-handles"
						width={cw}
						height={ch}
						viewBox="0 0 {cw} {ch}"
					>
						{#each editingPortsPx(peq) as port, i (i)}
							{#if port.dir}
								{@const [tx, ty] = tickEnd(port)}
								<line class="porttick" x1={port.x} y1={port.y} x2={tx} y2={ty} style="stroke-width: {1.5 / scale}" />
							{/if}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<circle
								class="porthandle"
								data-id={port.name}
								data-kind="port"
								data-equip={peq.id}
								class:sel={portsSelected === i}
								cx={port.x}
								cy={port.y}
								r={handleR}
								onpointerdown={(e) => portDown(e, peq, i)}
								ondblclick={(e) => {
									e.stopPropagation();
									portDelete(peq, i);
								}}
								><title>{port.name} · {fmtFraction(port.fx, port.fy)}{port.dir ? ` · exits ${port.dir}` : ''}</title></circle
							>
						{/each}
					</svg>
				{/if}
			{/if}

			{#each doc.labels ?? [] as l, i (i)}
				{@const pos = dispLabel(i, l)}
				{@const live = labelLive(l)}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<span
					class="lbl"
					data-id={i}
					data-kind="label"
					class:readout={live !== null}
					class:sel={selected('label', i)}
					style="left: {pos.x}px; top: {pos.y}px"
					onpointerdown={(e) => labelDown(e, i, l)}
					>{#if live !== null}{#if l.text}{l.text}{/if}<b class="num">{live}</b
						>{#if l.unit}<span>{l.unit}</span>{/if}{:else}{l.text}{/if}</span
				>
			{/each}

			{#if drag?.kind === 'box' && drag.moved}
				<div
					class="selbox"
					style="left: {Math.min(drag.x0, drag.x1)}px; top: {Math.min(drag.y0, drag.y1)}px; width: {Math.abs(drag.x1 - drag.x0)}px; height: {Math.abs(drag.y1 - drag.y0)}px; border-width: {1 / scale}px"
				></div>
			{/if}

			{#if ed.tool === 'place' && ed.placeComponent && cursor}
				{@const G = registry[ed.placeComponent]}
				<div class="ghost" style="left: {cursor[0]}px; top: {cursor[1]}px">
					{#if G}<G {...DEMO_PROPS[ed.placeComponent] ?? {}} />{:else}<UserIsland name={ed.placeComponent} props={DEMO_PROPS[ed.placeComponent] ?? {}} />{/if}
				</div>
			{/if}
		</div>
		<!-- The canvas's own resize handles: in the sizer's margin just past
		     the right/bottom edge (the canvas clips its children), in pane
		     pixels, so they keep their size at any zoom. -->
		{#if ed.tool === 'select' && !ed.portsEdit}
			{#each ['e', 's', 'se'] as const as edge (edge)}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div
					class="crs crs-{edge}"
					style="--cx: {cw * scale}px; --cy: {ch * scale}px"
					title="drag to resize the canvas"
					onpointerdown={(e) => canvasResizeDown(e, edge)}
				></div>
			{/each}
			{#if drag?.kind === 'canvas'}
				<span class="csize" style="left: {cw * scale}px; top: {ch * scale}px">{cw} × {ch}</span>
			{/if}
		{/if}
		</div>
	{:else}
		<p class="empty">waiting for document…</p>
	{/if}
</div>
{#if doc}
	<div class="zctl" role="toolbar" aria-label="mimic zoom">
		<button title="Zoom in (Ctrl+= / Ctrl+wheel)" aria-label="zoom in" onclick={zoomIn} disabled={scale >= MAX_ZOOM}>
			<svg viewBox="0 0 32 32"><path d="M32 18.133H18.133V32h-4.266V18.133H0v-4.266h13.867V0h4.266v13.867H32z" /></svg>
		</button>
		<button title="Zoom out (Ctrl+- / Ctrl+wheel)" aria-label="zoom out" onclick={zoomOut} disabled={scale <= MIN_ZOOM}>
			<svg viewBox="0 0 32 5"><path d="M0 0h32v4.2H0z" /></svg>
		</button>
		<button title="Fit the canvas to the pane width (Ctrl+0)" aria-label="fit view" onclick={zoomFit}>
			<svg viewBox="0 0 32 30"><path d="M3.692 4.63c0-.53.4-.938.939-.938h5.215V0H4.708C2.13 0 0 2.054 0 4.63v5.216h3.692V4.631zM27.354 0h-5.2v3.692h5.17c.53 0 .984.4.984.939v5.215H32V4.631A4.624 4.624 0 0027.354 0zm.954 24.83c0 .532-.4.94-.939.94h-5.215v3.768h5.215c2.577 0 4.631-2.13 4.631-4.707v-5.139h-3.692v5.139zm-23.677.94c-.531 0-.939-.4-.939-.94v-5.138H0v5.139c0 2.577 2.13 4.707 4.708 4.707h5.138V25.77H4.631z" /></svg>
		</button>
		<span class="zpct" title={zoom === null ? 'fitted to the pane width' : 'zoom level (this panel only, not saved to the file)'}>{Math.round(scale * 100)}%</span>
	</div>
{/if}
</div>

{#if portsEditEq}
	<PortsPanel
		ports={resolvePorts(portsEditEq, ed.manifest)}
		selected={portsSelected}
		onSelect={(i) => (portsSelected = i)}
		onRename={(i, name) => renamePortEC(portsEditEq, i, name)}
		onDelete={(i) => portDelete(portsEditEq, i)}
		onAdd={() => addPortAtFreeSlotEC(portsEditEq)}
		onSetDir={(i, dir) => setPortDirEC(portsEditEq, i, dir)}
	/>
{/if}

<style>
	.cpane {
		position: relative;
		flex: 1;
		min-width: 0;
		display: flex;
	}
	.wrap {
		flex: 1;
		min-width: 0;
		overflow: auto;
		padding: 12px;
		position: relative;
	}
	.sizer {
		position: relative;
		overflow: hidden;
		/* room under the corner zoom controls to scroll the canvas clear */
		margin-bottom: 110px;
	}
	/* Same spot and look as ZoomPane's controls (the ladder and SFC
	   editors) and FBD's xyflow Controls. */
	.zctl {
		position: absolute;
		left: 15px;
		bottom: 15px;
		z-index: 12;
		display: flex;
		flex-direction: column;
		align-items: stretch;
		box-shadow: var(--nx-shadow);
		border: 1px solid var(--nx-border);
		background: var(--nx-panel-bg);
	}
	.zctl button {
		display: flex;
		justify-content: center;
		align-items: center;
		width: 26px;
		height: 26px;
		padding: 4px;
		border: none;
		border-bottom: 1px solid var(--nx-border);
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
	}
	.zctl button:hover {
		background: var(--nx-ctl-hover);
	}
	.zctl button:disabled {
		cursor: default;
		opacity: 0.4;
	}
	.zctl svg {
		width: 100%;
		max-width: 12px;
		max-height: 12px;
		fill: currentColor;
	}
	.zpct {
		font-size: 9px;
		text-align: center;
		padding: 2px 0;
		color: var(--nx-muted);
		font-variant-numeric: tabular-nums;
	}
	.canvas {
		position: relative;
		transform-origin: top left;
		/* a drag that starts on equipment would otherwise also select its
		   caption text (the "P-101" under a pump turns into a blue
		   highlight mid-move); every canvas text is edited in the inspector */
		user-select: none;
		/* the card the HMI page puts a mimic on, in the kit's own palette */
		background-color: var(--surface);
		background-image:
			linear-gradient(to right, var(--grid) 1px, transparent 1px),
			linear-gradient(to bottom, var(--grid) 1px, transparent 1px);
		background-size: var(--grid-px) var(--grid-px);
		border: 1px solid var(--nx-border);
		box-shadow: var(--nx-shadow);
		/* Geometry (pipe points, equipment positions) is NEVER clamped to the
		   canvas rect by the reducer — a user may draw past the edge and
		   resize the canvas to fit later. Clipping is purely a render
		   concern, so it belongs here, not in mimicOps.ts. inset(-6px)
		   clips a few px past the border rather than exactly AT it, so a
		   port dot / selection halo legitimately sitting on the edge isn't
		   sliced in half — it still stops anything meaningfully out of
		   bounds from painting past the frame. */
		clip-path: inset(-6px);
	}
	.canvas.crosshair {
		cursor: crosshair;
	}
	.pipes {
		position: absolute;
		inset: 0;
		pointer-events: none;
		/* Was `visible`, which let pipe geometry beyond the canvas rect (or a
		   canvas since shrunk to no longer contain it) paint past the frame
		   with nothing to stop it — the parent .canvas clip-path above is
		   the actual backstop now; this just avoids double-clipping right at
		   the SVG's own edge. */
		overflow: hidden;
	}
	/* A selected pipe's vertex/midpoint handles — see the template comment
	   just above where this renders: a SEPARATE layer from .pipes, placed
	   AFTER the equipment boxes in the DOM specifically so a terminal handle
	   anchored to a port (which sits exactly on/over its equipment's own
	   box) paints ON TOP of it (BUG 2) instead of being hidden underneath. */
	.pipe-handles {
		position: absolute;
		inset: 0;
		pointer-events: none;
		overflow: hidden;
	}
	/* BUG 2's z-order fix, applied to the ports editor's dots too — see the
	   template comment where this renders: its own layer, AFTER the
	   equipment boxes, so a dot sitting on its equipment's own edge wins a
	   dead-center click instead of losing it to the box underneath. */
	.portedit-handles {
		position: absolute;
		inset: 0;
		pointer-events: none;
		overflow: hidden;
	}
	.hit {
		fill: none;
		stroke: transparent;
		pointer-events: stroke;
		cursor: pointer;
	}
	.pipesel {
		fill: none;
		stroke: var(--nx-sel-bg);
	}
	/* An anchor ref that doesn't resolve (Feature 2) — still drawn, just
	   flagged so it's obviously not a dead end. */
	.piperun.flagged :global(path) {
		filter: drop-shadow(0 0 0 var(--nx-err));
	}
	.piperun.flagged .hit {
		stroke: var(--nx-err);
		stroke-opacity: 0.25;
	}
	.draft {
		fill: none;
		stroke: var(--nx-accent);
		stroke-dasharray: 6 4;
	}
	/* The provisional "next click would go here" leg — fainter than the
	   committed-shape preview so the two read as different (this one isn't
	   part of what Enter creates). */
	.draft.pending {
		opacity: 0.4;
	}
	.draftpt {
		fill: var(--nx-accent);
	}
	.draftpt.anchor {
		fill: var(--nx-sel-ink);
		stroke: var(--nx-sel-bg);
		stroke-width: 1.5px;
	}
	.port {
		fill: var(--nx-bg);
		stroke: var(--nx-ok);
		stroke-width: 1.5px;
	}
	.port.hover {
		fill: var(--nx-sel-bg);
		stroke: var(--nx-sel-ink);
	}
	.porttick {
		stroke: var(--nx-ok);
		pointer-events: none;
	}
	.vtx {
		fill: var(--nx-bg);
		stroke: var(--nx-accent);
		stroke-width: 1.5px;
		pointer-events: all;
		cursor: grab;
	}
	.vtx.sel {
		fill: var(--nx-sel-bg);
		stroke: var(--nx-sel-ink);
		stroke-width: 2px;
	}
	/* A pipe's anchored terminal handle — a RING (hollow, thicker stroke)
	   with a small solid center dot (.anchordot, drawn right after it in the
	   template) rather than a plain filled vertex: reads as "pinned to
	   something" rather than "a point you placed", and stays visually
	   distinct from both a plain .vtx and the equipment's own small .port
	   dot. Larger than a plain vertex too (see the r={handleR*1.5} in the
	   template) so it's an easier target — dragging it off detaches rather
	   than just moving a point, so it's worth the extra precision margin. */
	.vtx.anchor {
		fill: none;
		stroke: var(--nx-ok);
		stroke-width: 2px;
	}
	/* BUG 3 sweep: needs its OWN rule (not just the plain `.vtx.sel` above) —
	   same specificity as `.vtx.anchor` (two classes each), so without a
	   THIRD class in the selector to break the tie, source order alone would
	   decide, leaving this one silently unstyled if `.vtx.anchor` ever moved
	   below it. Ink-colored stroke reads as "selected" the same way the
	   plain vertex's does, while keeping the ring (never filled) — an
	   anchor's whole visual point is "not a placed point". */
	.vtx.anchor.sel {
		stroke: var(--nx-sel-ink);
	}
	.anchordot {
		fill: var(--nx-ok);
		pointer-events: none;
	}
	.anchordot.sel {
		fill: var(--nx-sel-ink);
	}
	.mid {
		fill: var(--nx-accent);
		opacity: 0.55;
		pointer-events: all;
		cursor: copy;
	}
	.porthandle {
		fill: var(--nx-sel-bg);
		stroke: var(--nx-sel-ink);
		stroke-width: 2px;
		pointer-events: all;
		cursor: grab;
	}
	/* BUG 3 sweep: `class:sel={portsSelected === i}` (template, below) was
	   already wired up, but every `.porthandle` — selected or not — used the
	   sel-bg/sel-ink colors as its BASE style, so the toggle changed nothing
	   visible; the panel-row <-> dot selection (0.9.13) had no dot-side
	   effect. Mirrors PreviewApp.svelte's `.port.sel` (the preview
	   pane's identical dot, which got this right): the
	   SELECTED one goes fully opaque ink instead of the translucent sel-bg
	   wash every dot already wears, so only it reads as "picked". */
	.porthandle.sel {
		fill: var(--nx-sel-ink);
	}
	.eq {
		position: absolute;
		cursor: grab;
	}
	/* A bare <svg> root is inline by default, reserving a few px of
	   descender space below it inside `.eq` — the same offsetHeight-vs-
	   rendered-box mismatch the retired standalone component editor
	   had in its ports, just without the placeholder's
	   border/padding half of that bug (equipment here is always REAL
	   content, never a fixed-size placeholder box). block makes eqBox()'s
	   measurement match the visible shape exactly, so ports on the bottom
	   edge land ON it instead of just below. */
	.eq :global(svg) {
		display: block;
	}
	.eq.dragging {
		cursor: grabbing;
	}
	/* `behind` equipment (a wet well with pumps in it) paints under the
	   pipe layer — see the runtime <Mimic>'s identical rule. */
	.eq.behind {
		z-index: -1;
	}
	.eq.sel {
		outline: 2px solid var(--nx-accent);
		outline-offset: 3px;
		border-radius: 4px;
	}
	/* Resize handles sit on the selection outline (offset 3px): the right
	   edge's middle, the bottom edge's middle, and the corner. --hs is a
	   constant on-screen size (8px divided by the canvas scale). */
	.rs {
		position: absolute;
		width: var(--hs);
		height: var(--hs);
		background: var(--nx-accent);
		border: 1px solid var(--nx-bg, #1e1e1e);
		border-radius: 2px;
		box-sizing: border-box;
	}
	.rs-e {
		right: calc(-4px - var(--hs) / 2);
		top: calc(50% - var(--hs) / 2);
		cursor: ew-resize;
	}
	.rs-s {
		bottom: calc(-4px - var(--hs) / 2);
		left: calc(50% - var(--hs) / 2);
		cursor: ns-resize;
	}
	.rs-se {
		right: calc(-4px - var(--hs) / 2);
		bottom: calc(-4px - var(--hs) / 2);
		cursor: nwse-resize;
	}
	/* Canvas edge handles: a strip along the right and bottom edges and a
	   grip at the corner, quiet until hovered or dragged. */
	.crs {
		position: absolute;
		box-sizing: border-box;
	}
	.crs-e {
		left: var(--cx);
		top: 0;
		width: 8px;
		height: var(--cy);
		cursor: ew-resize;
	}
	.crs-s {
		left: 0;
		top: var(--cy);
		width: var(--cx);
		height: 8px;
		cursor: ns-resize;
	}
	.crs-se {
		left: var(--cx);
		top: var(--cy);
		width: 10px;
		height: 10px;
		cursor: nwse-resize;
		border-right: 2px solid var(--nx-border);
		border-bottom: 2px solid var(--nx-border);
	}
	.crs-e:hover,
	.crs-s:hover,
	.sizing .crs-e,
	.sizing .crs-s {
		background: color-mix(in srgb, var(--nx-accent) 35%, transparent);
	}
	.crs-se:hover,
	.sizing .crs-se {
		border-color: var(--nx-accent);
	}
	.csize {
		position: absolute;
		transform: translate(-100%, -100%) translate(-4px, -4px);
		padding: 1px 5px;
		font: 11px var(--vscode-editor-font-family, monospace);
		background: var(--nx-accent);
		color: var(--nx-bg, #1e1e1e);
		border-radius: 3px;
		pointer-events: none;
		white-space: nowrap;
	}
	.eq.portsedit {
		outline: 2px dashed var(--nx-sel-ink);
		outline-offset: 3px;
		border-radius: 4px;
		cursor: copy;
	}
	.selbox {
		position: absolute;
		border: 1px dashed var(--nx-accent);
		background: color-mix(in srgb, var(--nx-accent) 10%, transparent);
		pointer-events: none;
		box-sizing: border-box;
	}
	.lbl {
		position: absolute;
		font-size: 12px;
		color: var(--nx-muted);
		white-space: nowrap;
		cursor: grab;
		padding: 1px 2px;
	}
	/* A bound label with a live value reads as the runtime's readout chip
	   (Mimic.svelte's .lbl.readout): muted text, the value in ink on a pill. */
	.lbl.readout {
		display: inline-flex;
		align-items: baseline;
		gap: 0.35em;
		padding: 2px 7px;
		background: var(--nx-panel-bg);
		border: 1px solid var(--nx-border);
		border-radius: 6px;
	}
	.lbl.readout .num {
		color: var(--nx-ui-ink);
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}
	.lbl.sel {
		outline: 2px solid var(--nx-accent);
		outline-offset: 2px;
		border-radius: 3px;
	}
	.ghost {
		position: absolute;
		opacity: 0.55;
		pointer-events: none;
	}
	.empty {
		color: var(--nx-muted);
		padding: 24px;
	}
</style>
