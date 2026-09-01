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
	import {
		makeGetPort,
		Pipe,
		pointsToPath,
		resolveBindings,
		resolvedPortDir,
		resolvePipeEndpoints,
		routedPoints
	} from '@joyautomation/nautilus-hmi';
	import type { GetPort, MimicEquipment, MimicPipe, MimicPipeAnchor, PortDir } from '@joyautomation/nautilus-hmi';
	import { DEMO_PROPS, registry } from './registry';
	import { resolvePorts, type Port } from './ports';
	import { fmtFraction, newPortAtFreeSlot, newPortAtPoint, nudgePort, roundPort, toFraction } from './portsGestures';
	import { minInteriorPoints, resolveDraftFinish, type NamedPort } from './pipeDraft';
	import { suggestRoute, type ObstacleRect } from './autoroute';
	import { ed, GRID, postManifestOp, postOp, snap, type MimicOp } from './mimicState.svelte';
	import PortsPanel from './PortsPanel.svelte';
	import UserIsland from './UserIsland.svelte';

	const doc = $derived(ed.doc);

	// ── scale to fit ────────────────────────────────────────────────────────
	let wrap = $state<HTMLDivElement | null>(null);
	let scale = $state(1);
	$effect(() => {
		if (!wrap || !doc) return;
		const el = wrap;
		const w = doc.canvas.width;
		const update = () => (scale = Math.min(1.5, Math.max(0.1, (el.clientWidth - 24) / w)));
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});

	let canvasEl = $state<HTMLDivElement | null>(null);
	function pt(e: { clientX: number; clientY: number }): [number, number] {
		const r = canvasEl!.getBoundingClientRect();
		return [(e.clientX - r.left) / scale, (e.clientY - r.top) / scale];
	}

	// ── equipment boxes & ports ─────────────────────────────────────────────
	// Rendered sizes come from the DOM (offsetWidth is in canvas units —
	// the scale transform doesn't affect it).
	const eqEls = new Map<string, HTMLElement>();
	function regEl(node: HTMLElement, id: string) {
		eqEls.set(id, node);
		return {
			update(next: string) {
				eqEls.delete(id);
				id = next;
				eqEls.set(id, node);
			},
			destroy() {
				eqEls.delete(id);
			}
		};
	}

	/** `eq`'s rendered box — position from dispEq()'s SAME live-drag/settled
	 * override `eq`'s own div uses for its `left/top` style (so a pipe
	 * anchored to this equipment's port re-derives its position from the
	 * box LIVE, in step with the equipment, while an 'eq' drag is still in
	 * flight — not just after it lands), size DOM-measured. */
	function eqBox(eq: MimicEquipment): { x: number; y: number; w: number; h: number } {
		const el = eqEls.get(eq.id);
		const pos = dispEq(eq);
		return {
			x: pos.x,
			y: pos.y,
			w: el?.offsetWidth || eq.width || 100,
			h: el?.offsetHeight || 80
		};
	}

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
		const b = eqBox(eq);
		return spec.map((p) => ({
			name: p.name,
			x: Math.round(b.x + p.x * b.w),
			y: Math.round(b.y + p.y * b.h),
			fx: p.x,
			fy: p.y,
			dir: resolvedPortDir(p)
		}));
	}

	/** Ports currently shown by the ports editor for `eq` — the resolved set,
	 * with the in-flight drag (if any) substituted in — as canvas pixels. */
	function editingPortsPx(eq: MimicEquipment): PortPx[] {
		const b = eqBox(eq);
		const fr = resolvePorts(eq, ed.manifest).map((p) => ({ ...p }));
		if (drag?.kind === 'port' && drag.id === eq.id) fr[drag.index] = { ...fr[drag.index], x: drag.fx, y: drag.fy };
		return fr.map((p) => ({
			name: p.name,
			x: Math.round(b.x + p.x * b.w),
			y: Math.round(b.y + p.y * b.h),
			fx: p.x,
			fy: p.y,
			dir: resolvedPortDir(p)
		}));
	}

	/** Direction-tick endpoint (unit vector * length) for a resolved port —
	 * the small arrow drawn off a port dot showing its effective exit
	 * direction (Feature 1). Shared constant with ComponentApp.svelte's own
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
		const b = eqBox(eq);
		const existing = resolvePorts(eq, ed.manifest);
		commitPorts(eq, [...existing, newPortAtPoint(b, raw, existing)]);
	}

	const PORT_SNAP = 14;
	/** A cursor point -> the nearest port within snap radius, WITH its owning
	 * equipment id + port name (not just its coordinates) — the seam pipe
	 * drawing/dragging use to record an anchor rather than a raw point.
	 * (NamedPort's shape is imported from pipeDraft.ts — the one other place
	 * that needs to reason about a "point that might be a port".) */
	function portSnapNamed(p: [number, number]): NamedPort | null {
		for (const eq of doc?.equipment ?? []) {
			for (const port of eqPorts(eq)) {
				if (Math.hypot(port.x - p[0], port.y - p[1]) <= PORT_SNAP) {
					return { equip: eq.id, port: port.name, x: port.x, y: port.y };
				}
			}
		}
		return null;
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
		}
	);

	/** Keep-out rectangles for autoroute.ts's obstacle avoidance: every
	 * equipment box EXCEPT the ones the new pipe connects to (routing away
	 * from your OWN endpoint equipment doesn't make sense — the port sits
	 * ON its edge), expanded by a fixed margin so a suggested route clears
	 * equipment by a comfortable gap rather than grazing it. */
	const ROUTE_MARGIN = 16;
	function obstaclesFor(excludeIds: Set<string>): ObstacleRect[] {
		return (doc?.equipment ?? [])
			.filter((eq) => !excludeIds.has(eq.id))
			.map((eq) => {
				const b = eqBox(eq);
				return { x: b.x - ROUTE_MARGIN, y: b.y - ROUTE_MARGIN, w: b.w + 2 * ROUTE_MARGIN, h: b.h + 2 * ROUTE_MARGIN };
			});
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
	function materializeAnchorsFor(eqId: string): MimicOp[] {
		const ops: MimicOp[] = [];
		for (const p of doc?.pipes ?? []) {
			const fromHit = p.from?.equip === eqId;
			const toHit = p.to?.equip === eqId;
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
	function deleteEquipmentWithAnchors(eqId: string) {
		const matOps = materializeAnchorsFor(eqId);
		const del: MimicOp = { type: 'deleteEquipment', id: eqId };
		postOp(matOps.length ? { type: 'batch', ops: [...matOps, del] } : del);
	}

	// ── in-flight gestures ──────────────────────────────────────────────────
	type Drag =
		| { kind: 'eq'; id: string; dx: number; dy: number; ox: number; oy: number; x: number; y: number; atts: Attach[]; moved: boolean }
		| { kind: 'label'; index: number; dx: number; dy: number; x: number; y: number; moved: boolean }
		/** `index` is an INTERIOR index into the pipe's stored `points` — see
		 * pipeHandlePoints()'s doc comment for the full-vs-interior index split. */
		| { kind: 'vtx'; id: string; index: number; pts: [number, number][]; moved: boolean }
		| { kind: 'port'; id: string; index: number; fx: number; fy: number; moved: boolean }
		/** Dragging a pipe's terminal (start/end) handle — Feature 2's
		 * attach/detach gesture. `x`/`y` track the live cursor (port-snapped
		 * when near one); `port` is the port that snap currently lands on (null
		 * off any port) so the live preview can route EXACTLY what the drop will
		 * commit (a stub + orthogonal corner when it'll attach). Resolved on
		 * drop (onup) by re-checking that same position. */
		| { kind: 'anchor'; id: string; end: 'from' | 'to'; x: number; y: number; port: MimicPipeAnchor | null; moved: boolean };
	let drag = $state<Drag | null>(null);
	/** Row selection for the ports-edit side panel (PortsPanel, below) —
	 * mirrors ComponentApp.svelte's `selected`. Reset whenever ed.portsEdit
	 * changes (see the $effect near the template). */
	let portsSelected = $state<number | null>(null);

	/** Committed-but-unconfirmed positions: rendered until the doc catches
	 * up (or a refusal times out) so nothing flashes back on pointerup. */
	let settled = $state<null | {
		eq: Record<string, { x: number; y: number }>;
		pipes: Record<string, [number, number][]>;
		labels: Record<number, { x: number; y: number }>;
	}>(null);
	let settleTimer: ReturnType<typeof setTimeout> | undefined;
	function settle(part: {
		eq?: Record<string, { x: number; y: number }>;
		pipes?: Record<string, [number, number][]>;
		labels?: Record<number, { x: number; y: number }>;
	}) {
		settled = { eq: {}, pipes: {}, labels: {}, ...part };
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
			});
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
			: (settled?.eq[eq.id] ?? { x: eq.x, y: eq.y });
	const dispLabel = (i: number, l: { x: number; y: number }): { x: number; y: number } =>
		drag?.kind === 'label' && drag.index === i
			? { x: drag.x, y: drag.y }
			: (settled?.labels[i] ?? { x: l.x, y: l.y });
	const dispPts = (p: MimicPipe): [number, number][] => {
		if (drag?.kind === 'vtx' && drag.id === p.id) return drag.pts;
		if (drag?.kind === 'eq' && drag.atts.some((a) => a.pipeId === p.id)) {
			return shiftedPoints(p, drag.atts, drag.x - drag.ox, drag.y - drag.oy);
		}
		return settled?.pipes[p.id] ?? p.points;
	};

	const eqProps = (eq: MimicEquipment): Record<string, unknown> => ({
		...(DEMO_PROPS[eq.component] ?? {}),
		...(eq.width !== undefined ? { width: eq.width } : {}),
		...(eq.label !== undefined ? { label: eq.label } : {}),
		...(eq.props ?? {}),
		...resolveBindings(eq.bind, ed.tags ?? {})
	});

	// ── equipment / label drags ─────────────────────────────────────────────
	function eqDown(e: PointerEvent, eq: MimicEquipment) {
		if (ed.tool !== 'select' || e.button !== 0) return;
		e.stopPropagation();
		// BUG 1: see pipeDown's identical guard — a shift-click stray-hitting
		// equipment (easy when a pipe anchor sits right on its edge) must not
		// clobber an in-progress node multi-selection on some other pipe.
		if (e.shiftKey) return;
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

	function labelDown(e: PointerEvent, i: number, l: { x: number; y: number }) {
		if (ed.tool !== 'select' || e.button !== 0) return;
		e.stopPropagation();
		// BUG 1: see pipeDown's identical guard.
		if (e.shiftKey) return;
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
		drag = { kind: 'port', id: eq.id, index: i, fx: port?.x ?? 0.5, fy: port?.y ?? 0.5, moved: false };
	}

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
		} else if (drag.kind === 'port') {
			const eq = (doc?.equipment ?? []).find((e) => e.id === drag.id);
			if (eq) {
				const b = eqBox(eq);
				const [fx, fy] = toFraction(b, pt(e));
				if (fx !== drag.fx || fy !== drag.fy) {
					drag.fx = fx;
					drag.fy = fy;
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
		if (drag.moved) {
			if (drag.kind === 'eq') {
				const eq = (doc?.equipment ?? []).find((e) => e.id === (drag as { id: string }).id);
				if (eq) postEqMove(eq, drag.atts, drag.x, drag.y);
			} else if (drag.kind === 'label') {
				postOp({ type: 'updateLabel', index: drag.index, patch: { x: drag.x, y: drag.y } });
				settle({ labels: { [drag.index]: { x: drag.x, y: drag.y } } });
			} else if (drag.kind === 'port') {
				const eq = (doc?.equipment ?? []).find((e) => e.id === drag.id);
				if (eq) {
					const ports = resolvePorts(eq, ed.manifest).map((p) => ({ ...p }));
					ports[drag.index] = { ...ports[drag.index], x: drag.fx, y: drag.fy };
					commitPorts(eq, ports);
				}
			} else if (drag.kind === 'anchor') {
				const p = (doc?.pipes ?? []).find((pp) => pp.id === drag.id);
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
		// else (here, pipeDown, midDown) it's a no-op, never a wipe.
		if (e.shiftKey) return;
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
				const obstacles = obstaclesFor(new Set([result.from.equip, result.to.equip]));
				points = suggestRoute(startPort, startPort.dir, endPort, endPort.dir, obstacles);
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

	/** Finish the pipe-drawing draft (Enter or double-click): commit whatever
	 * draftSpec currently represents — identical to what the preview shows. */
	function finishPipe() {
		const spec = draftSpec;
		if (spec) {
			postOp({
				type: 'addPipe',
				points: spec.points,
				...(spec.from ? { from: spec.from } : {}),
				...(spec.to ? { to: spec.to } : {}),
				...(spec.routing ? { routing: spec.routing } : {})
			});
			ed.tool = 'select';
		}
		draft = [];
		draftFrom = null;
		draftLastSnap = null;
	}

	// ── keyboard ────────────────────────────────────────────────────────────
	function onkeydown(e: KeyboardEvent) {
		const t = e.target as HTMLElement | null;
		if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) return;
		if (e.key === 'Escape') {
			if (draft.length) {
				draft = [];
				draftFrom = null;
				draftLastSnap = null;
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
	const showPorts = $derived(ed.tool === 'pipe' || drag?.kind === 'vtx');
	/** `kind: 'end'` (a pipe's terminal handle selected) counts as its pipe
	 * being 'pipe'-selected for every visual/handles purpose below — it's
	 * only a distinct Selection variant so keyboard nudge can address the
	 * specific end (see the type's doc comment); nothing else needs to tell
	 * the two apart. */
	const selected = (kind: 'equipment' | 'pipe' | 'label', key: string | number): boolean => {
		const s = ed.selection;
		if (!s) return false;
		if (kind === 'pipe' && s.kind === 'end') return s.pipeId === key;
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

<div class="wrap" bind:this={wrap}>
	{#if doc}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="canvas"
			class:crosshair={ed.tool !== 'select'}
			bind:this={canvasEl}
			style="width: {doc.canvas.width}px; height: {doc.canvas.height}px; transform: scale({scale}); --grid-px: {GRID}px"
			onpointerdowncapture={onCanvasCapture}
			onpointerdown={canvasDown}
			ondblclick={() => ed.tool === 'pipe' && finishPipe()}
		>
			<svg
				class="pipes"
				width={doc.canvas.width}
				height={doc.canvas.height}
				viewBox="0 0 {doc.canvas.width} {doc.canvas.height}"
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
						<g class="piperun" class:sel={selected('pipe', p.id)} class:flagged={pipeFlagged(p)}>
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
						<path class="hit" {d} style="stroke-width: {14 / scale}" onpointerdown={(e) => pipeDown(e, p)} />
					</g>
				{/each}

				{#if showPorts}
					{#each doc.equipment ?? [] as eq (eq.id)}
						{#each eqPorts(eq) as port (port.name)}
							{@const isHover = hoverPort && hoverPort.equip === eq.id && hoverPort.port === port.name}
							{#if port.dir}
								{@const [tx, ty] = tickEnd(port)}
								<line class="porttick" x1={port.x} y1={port.y} x2={tx} y2={ty} style="stroke-width: {1.5 / scale}" />
							{/if}
							<circle class="port" class:hover={isHover} cx={port.x} cy={port.y} r={(isHover ? 6 : 4) / scale}
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
					class:sel={selected('equipment', eq.id)}
					class:dragging={drag?.kind === 'eq' && drag.id === eq.id}
					class:portsedit={ed.portsEdit?.id === eq.id}
					style="left: {pos.x}px; top: {pos.y}px"
					use:regEl={eq.id}
					onpointerdown={(e) => eqDown(e, eq)}
					ondblclick={(e) => eqDblclick(e, eq)}
				>
					{#if C}<C {...eqProps(eq)} />{:else}<UserIsland name={eq.component} props={eqProps(eq)} />{/if}
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
				width={doc.canvas.width}
				height={doc.canvas.height}
				viewBox="0 0 {doc.canvas.width} {doc.canvas.height}"
			>
				{#each doc.pipes ?? [] as p (p.id)}
					{#if pipeHandlesVisible(p.id)}
						{@const pts = pipeHandlePoints(p)}
						{#each pts.slice(0, -1) as [x1, y1], i (i)}
							{@const [x2, y2] = pts[i + 1]}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<rect
								class="mid"
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
						width={doc.canvas.width}
						height={doc.canvas.height}
						viewBox="0 0 {doc.canvas.width} {doc.canvas.height}"
					>
						{#each editingPortsPx(peq) as port, i (i)}
							{#if port.dir}
								{@const [tx, ty] = tickEnd(port)}
								<line class="porttick" x1={port.x} y1={port.y} x2={tx} y2={ty} style="stroke-width: {1.5 / scale}" />
							{/if}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<circle
								class="porthandle"
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
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<span
					class="lbl"
					class:sel={selected('label', i)}
					style="left: {pos.x}px; top: {pos.y}px"
					onpointerdown={(e) => labelDown(e, i, l)}>{l.text}</span
				>
			{/each}

			{#if ed.tool === 'place' && ed.placeComponent && cursor}
				{@const G = registry[ed.placeComponent]}
				<div class="ghost" style="left: {cursor[0]}px; top: {cursor[1]}px">
					{#if G}<G {...DEMO_PROPS[ed.placeComponent] ?? {}} />{:else}<UserIsland name={ed.placeComponent} props={DEMO_PROPS[ed.placeComponent] ?? {}} />{/if}
				</div>
			{/if}
		</div>
	{:else}
		<p class="empty">waiting for document…</p>
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
	.wrap {
		flex: 1;
		min-width: 0;
		overflow: auto;
		padding: 12px;
		position: relative;
	}
	.canvas {
		position: relative;
		transform-origin: top left;
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
	   effect. Mirrors ComponentApp.svelte's `.port.sel` (the standalone
	   *.component.json editor's identical dot, which got this right): the
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
	   rendered-box mismatch as the standalone component editor (see
	   ComponentApp.svelte's ports fix), just without the placeholder's
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
	.eq.sel {
		outline: 2px solid var(--nx-accent);
		outline-offset: 3px;
		border-radius: 4px;
	}
	.eq.portsedit {
		outline: 2px dashed var(--nx-sel-ink);
		outline-offset: 3px;
		border-radius: 4px;
		cursor: copy;
	}
	.lbl {
		position: absolute;
		font-size: 12px;
		color: var(--nx-muted);
		white-space: nowrap;
		cursor: grab;
		padding: 1px 2px;
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
