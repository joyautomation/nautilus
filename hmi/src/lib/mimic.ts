// The mimic document: a P&ID-style process graphic as DATA — equipment
// placed on a canvas, pipe runs routed between them, live tag bindings.
// It is to the HMI what a .fbd file is to the program: a domain artifact
// that renders live (<Mimic>) and that graphical tooling can edit, while
// the file itself stays text — diffable, reviewable, versioned. Screens,
// routing, and app logic stay ordinary SvelteKit; a mimic is one component
// on a page, for the parts of an HMI that are genuinely spatial.

/** One mimic document (typically a *.mimic.json file imported by a page). */
export interface MimicDoc {
	/** Optional display name. */
	name?: string;
	/** Logical canvas size; the renderer scales it to its container. */
	canvas: { width: number; height: number };
	equipment?: MimicEquipment[];
	pipes?: MimicPipe[];
	labels?: MimicLabel[];
}

/** The direction a pipe LEAVES a port, in canvas space. */
export type PortDir = 'left' | 'right' | 'up' | 'down';

/** A named connection point, as a fraction of the rendered box. The name is
 * how a pipe end anchors to it explicitly (MimicPipe.from/to — see below).
 * `dir` is the direction a pipe leaves the port; when absent it's INFERRED
 * from the port's edge position (see inferPortDir) — explicit `dir` is only
 * needed to override that inference or to give a corner/interior port a
 * direction it has none of by default. Names are unique within a
 * component's port list. */
export interface MimicPort {
	name: string;
	x: number;
	y: number;
	dir?: PortDir;
}

/** A port's exit direction, inferred from its position on the rendered
 * box's edge: `x === 0` -> left, `x === 1` -> right, `y === 0` -> up,
 * `y === 1` -> down. A port sitting exactly on a CORNER (both x and y at an
 * edge) or fully in the INTERIOR (neither) has no inferred direction —
 * exactly one axis must be pinned to an edge. Implemented once here so the
 * mimic editor (EditorCanvas.svelte, PortsPanel.svelte) and the runtime
 * (<Mimic>) always agree on what "auto" resolves to. */
export function inferPortDir(port: Pick<MimicPort, 'x' | 'y'>): PortDir | undefined {
	const xEdge = port.x === 0 || port.x === 1;
	const yEdge = port.y === 0 || port.y === 1;
	if (xEdge === yEdge) return undefined; // both true (corner) or both false (interior)
	return xEdge ? (port.x === 0 ? 'left' : 'right') : port.y === 0 ? 'up' : 'down';
}

/** A port's EFFECTIVE exit direction: the explicit `dir` when set, else the
 * inferred one (or undefined for a corner/interior port with no override). */
export function resolvedPortDir(port: Pick<MimicPort, 'x' | 'y' | 'dir'>): PortDir | undefined {
	return port.dir ?? inferPortDir(port);
}

/** The kit built-ins' default connection points, as fractions of the
 * rendered box — the same table the mimic editor's registry.ts/
 * builtinPorts.ts re-exports (that file now just points here so both tiers
 * — the editor's resolvePorts() instance -> sidecar -> builtin chain, and
 * the runtime's instance -> builtin chain, resolveRuntimePorts() below —
 * agree on the builtin tier without duplicating the data). */
export const BUILTIN_PORTS: Record<string, MimicPort[]> = {
	Tank: [
		{ name: 'top', x: 0.5, y: 0 },
		{ name: 'left', x: 0, y: 0.5 },
		{ name: 'right', x: 1, y: 0.5 },
		{ name: 'bottom', x: 0.5, y: 1 }
	],
	// The level-only reservoir (LevelTank) is not one of <Mimic>'s built-in
	// components — it reaches a doc through the `registry` prop — but its
	// ports belong here all the same: BUILTIN_PORTS is keyed by the
	// component NAME a doc writes, and resolveRuntimePorts() consults it for
	// whatever that name resolves to. Its rendered box is the vessel itself
	// when `label` is empty (the caption is the only thing below the svg), so
	// these fractions land on the vessel walls exactly; a doc that captions
	// the tank should override them per instance rather than let the caption
	// push the bottom port off the vessel.
	LevelTank: [
		{ name: 'top', x: 0.5, y: 0 },
		{ name: 'left', x: 0, y: 0.5 },
		{ name: 'right', x: 1, y: 0.5 },
		{ name: 'bottom', x: 0.5, y: 1 }
	],
	Pump: [
		{ name: 'in', x: 0, y: 0.5 },
		{ name: 'out', x: 1, y: 0.5 }
	],
	Valve: [
		{ name: 'in', x: 0, y: 0.5 },
		{ name: 'out', x: 1, y: 0.5 }
	],
	Gauge: [],
	Sparkline: []
};

/** Resolve an equipment instance's connection points at the RUNTIME's
 * precedence tier: instance override (`eq.ports`) -> built-in default. No
 * manifest/sidecar tier — *.component.json sidecars are a project/editor-
 * time aggregation (mimicComponentIndex.ts) that never ships with a built
 * HMI, so the runtime can't see them; only what's embedded in the doc
 * itself (or the shared built-in table) is available here. The mimic
 * editor's own resolvePorts() (webview-ui/src/mimic/ports.ts) additionally
 * consults the sidecar manifest tier before falling back to this same
 * built-in table — so an anchor/dir that depends on a sidecar override
 * renders correctly in the editor but NOT identically at runtime; keep
 * anything that must render identically in both (like a demo's) on the
 * instance-`ports` or built-in tiers instead. */
export function resolveRuntimePorts(eq: Pick<MimicEquipment, 'component' | 'ports'>): MimicPort[] {
	if (eq.ports !== undefined) return eq.ports;
	return BUILTIN_PORTS[eq.component] ?? [];
}

/** A placed component: a kit component (Tank, Pump, Valve, Gauge, …) or a
 * custom one supplied through the Mimic `registry` prop. */
export interface MimicEquipment {
	id: string;
	/** Registry name of the component to render. */
	component: string;
	/** Top-left position in canvas units. */
	x: number;
	y: number;
	/** Rendered width in canvas units (component default when omitted). */
	width?: number;
	label?: string;
	/** Static props passed straight through. */
	props?: Record<string, unknown>;
	/** Live bindings: component prop -> tag name. Resolved against the
	 * frame's tags each render; an absent tag leaves the prop unset. */
	bind?: Record<string, string>;
	/** Connection-point override, as named fractions of the rendered box.
	 * Drives pipe-snapping/attachment in the VS Code mimic editor (see
	 * tools/vscode-iec's registry PORTS + the {ComponentName}.component.json
	 * sidecar convention) AND, since it lives in the doc itself, is also
	 * what the runtime <Mimic> resolves a pipe ANCHOR's (MimicPipe.from/to)
	 * port against — see resolveRuntimePorts() above (the runtime does NOT
	 * additionally consult the sidecar tier; only this instance-level
	 * override or the built-in default). Absent means "inherit" — the
	 * sidecar entry for `component` (editor only) else the built-in default;
	 * an explicit `[]` means "no ports" and is NOT the same as absent. */
	ports?: MimicPort[];
}

/** A pipe end's attachment to a named equipment port. When set, that end's
 * position is DERIVED (the port's resolved absolute position) rather than
 * stored — MimicPipe.points holds only the INTERIOR vertices between the
 * two ends (empty when both ends are anchored with no bend between them),
 * never the anchored endpoint itself, so an anchored pipe can't go stale
 * when its equipment moves. `equip` need not resolve to an existing
 * equipment id / `port` need not be a port that equipment actually has —
 * an unresolvable anchor still renders (falling back to the nearest
 * interior vertex, or the other end) rather than disappearing; see
 * resolvePipeEndpoints(). */
export interface MimicPipeAnchor {
	equip: string;
	port: string;
}

/** A pipe run: a polyline in canvas units. Bind `flowing` (and optionally
 * `rate`) to tags to animate flow with the process — or `dead` (see
 * Pipe.svelte) for the run whose meter the runtime does not publish, which is
 * NOT the same as a run that is standing still. */
export interface MimicPipe {
	id: string;
	/** Interior vertices only when an end is anchored (see `from`/`to`) —
	 * otherwise (the common case) the full point list, same as always. */
	points: [number, number][];
	color?: string;
	/** How consecutive points are connected: a straight segment between each
	 * pair ('direct', the default when absent — a hand-drawn point list is
	 * already whatever shape the user drew) or an orthogonal (90°) L between
	 * each pair ('orthogonal' — see routedPoints()). This is a document
	 * property, not an editor preference, because both the VS Code mimic
	 * editor and the runtime <Mimic> render pipes and must draw identically. */
	routing?: 'direct' | 'orthogonal';
	/** Anchor the pipe's first/last point to a named equipment port instead
	 * of a stored [x, y] — see MimicPipeAnchor. Absent = a plain point (or
	 * no point at all if `points` is also empty on that end, which is only
	 * valid when the OTHER end supplies it). */
	from?: MimicPipeAnchor;
	to?: MimicPipeAnchor;
	props?: Record<string, unknown>;
	bind?: Record<string, string>;
}

/** A free text label on the canvas. Give it a `bind` and it becomes a live
 * readout: `text` (when non-empty) prefixes the tag's value, formatted to
 * `decimals` places with `unit` appended — "LT-101 67.3 %". Until the tag
 * resolves to a number the value renders as the kit's usual '—'. Without
 * `bind` it's the plain static text it has always been. */
export interface MimicLabel {
	text: string;
	x: number;
	y: number;
	/** Tag to read live — the same tag-name form equipment `bind` values use
	 * (resolved through the same resolveBindings() below). */
	bind?: string;
	/** Unit suffix shown after the live value (only meaningful with `bind`). */
	unit?: string;
	/** Fraction digits for the live value (default 1). */
	decimals?: number;
}

/** Polyline points -> SVG path ("M x y L x y …"). */
export function pointsToPath(points: [number, number][]): string {
	return points.map(([x, y], i) => `${i === 0 ? 'M' : 'L'} ${x} ${y}`).join(' ');
}

/** Expand a point list into an orthogonal (90°-only) route: each consecutive
 * pair gets one right-angle corner between them instead of a diagonal.
 *
 * Corner rule (deterministic, so the editor and runtime always agree):
 * each segment continues the PREVIOUS segment's arrival axis first, then
 * turns onto the other axis to reach the next point — so a chain of
 * corners reads as a smooth Z rather than zigzagging back and forth. The
 * very first segment (no incoming direction yet) picks whichever axis has
 * the larger delta, the same "dominant axis" rule the editor's freehand
 * pipe-drawing snap already uses (EditorCanvas.svelte's nextPoint()).
 * A pair that's already axis-aligned (shares an x or y) needs no corner at
 * all and is passed through as one straight leg.
 *
 * Node positions themselves are never altered — only points BETWEEN them
 * are inserted — so hit-targets/handles keep using the original points. */
export function orthogonalPoints(points: [number, number][]): [number, number][] {
	if (points.length < 2) return points.slice();
	const out: [number, number][] = [points[0]];
	let lastAxis: 'h' | 'v' | null = null;
	for (let i = 1; i < points.length; i++) {
		const [x0, y0] = points[i - 1];
		const [x1, y1] = points[i];
		if (x0 === x1 || y0 === y1) {
			// Already orthogonal: no corner needed. A single point (x0===x1
			// AND y0===y1) leaves lastAxis as-is; otherwise it's whichever
			// axis stayed constant.
			out.push([x1, y1]);
			if (x0 !== x1 || y0 !== y1) lastAxis = x0 === x1 ? 'v' : 'h';
			continue;
		}
		const firstAxis: 'h' | 'v' = lastAxis ?? (Math.abs(x1 - x0) >= Math.abs(y1 - y0) ? 'h' : 'v');
		out.push(firstAxis === 'h' ? [x1, y0] : [x0, y1]);
		out.push([x1, y1]);
		lastAxis = firstAxis === 'h' ? 'v' : 'h'; // the second leg's axis arrives at point i
	}
	return out;
}

// ── port anchors + exit-direction stubs ─────────────────────────────────────

/** An equipment's rendered box in canvas pixels — top-left + measured size.
 * Both renderers measure this from the DOM (a component's actual rendered
 * size, not just its declared `width`, determines where a port fraction
 * lands) and feed it through portAbsolute()/makeGetPort() below. */
export interface EquipmentBox {
	x: number;
	y: number;
	w: number;
	h: number;
}

/** One end of a resolved pipe anchor: the port's absolute canvas position
 * plus its effective exit direction (see resolvedPortDir). */
export interface ResolvedPort {
	x: number;
	y: number;
	dir?: PortDir;
}

/** A port fraction, in an equipment's box -> its absolute canvas position +
 * effective direction. The one place a {name, x, y, dir} port becomes
 * pixels, so every caller (editor ports-mode dots, pipe anchor resolution)
 * agrees. */
export function portAbsolute(box: EquipmentBox, port: Pick<MimicPort, 'x' | 'y' | 'dir'>): ResolvedPort {
	return { x: box.x + port.x * box.w, y: box.y + port.y * box.h, dir: resolvedPortDir(port) };
}

/** Looks up one equipment's one named port, resolved to canvas pixels —
 * undefined when the equipment or the port doesn't exist (an unresolvable
 * anchor; see resolvePipeEndpoints). */
export type GetPort = (equip: string, port: string) => ResolvedPort | undefined;

/** Build a GetPort from a box lookup + a ports lookup — the seam both
 * renderers plug their own equipment/ports resolution into (the editor's
 * resolvePorts() instance -> sidecar -> builtin chain, or the runtime's
 * resolveRuntimePorts() instance -> builtin chain) without this module
 * needing to know about either. */
export function makeGetPort(
	getBox: (equip: string) => EquipmentBox | undefined,
	getPorts: (equip: string) => MimicPort[] | undefined
): GetPort {
	return (equip, port) => {
		const box = getBox(equip);
		if (!box) return undefined;
		const p = (getPorts(equip) ?? []).find((p) => p.name === port);
		if (!p) return undefined;
		return portAbsolute(box, p);
	};
}

/** A pipe's resolved endpoints: the full point list (anchors resolved to
 * absolute positions and prepended/appended to the stored INTERIOR points),
 * each anchored end's effective exit direction (for the stub — see
 * withDirStub below), and whether an anchor reference failed to resolve
 * (unknown equip/port — flagged for the editor's PropsPanel to offer a fix,
 * per the "editor is never stricter than the JSON" principle; the geometry
 * still renders rather than disappearing). */
export interface PipeEndpoints {
	points: [number, number][];
	startDir?: PortDir;
	endDir?: PortDir;
	startFlagged: boolean;
	endFlagged: boolean;
}

/** Resolve `pipe.from`/`pipe.to` against `getPort` and splice them onto the
 * stored interior points. An unresolved anchor falls back to the nearest
 * interior vertex on its side, or (with no interior points at all) the
 * OTHER end's resolved position, or (nothing resolves at all) the origin —
 * geometry always comes back complete, it just gets flagged. */
export function resolvePipeEndpoints(
	pipe: Pick<MimicPipe, 'points' | 'from' | 'to'>,
	getPort: GetPort
): PipeEndpoints {
	const interior = pipe.points.map((p) => [...p] as [number, number]);
	let startFlagged = false;
	let endFlagged = false;
	let startDir: PortDir | undefined;
	let endDir: PortDir | undefined;
	let startPoint: [number, number] | undefined;
	let endPoint: [number, number] | undefined;

	if (pipe.from) {
		const resolved = getPort(pipe.from.equip, pipe.from.port);
		if (resolved) {
			startPoint = [resolved.x, resolved.y];
			startDir = resolved.dir;
		} else {
			startFlagged = true;
		}
	}
	if (pipe.to) {
		const resolved = getPort(pipe.to.equip, pipe.to.port);
		if (resolved) {
			endPoint = [resolved.x, resolved.y];
			endDir = resolved.dir;
		} else {
			endFlagged = true;
		}
	}

	const points: [number, number][] = [];
	if (startPoint) points.push(startPoint);
	points.push(...interior);
	if (endPoint) points.push(endPoint);

	if (pipe.from && !startPoint) points.unshift(points[0] ?? endPoint ?? [0, 0]);
	if (pipe.to && !endPoint) points.push(points.at(-1) ?? startPoint ?? [0, 0]);

	return { points, startDir, endDir, startFlagged, endFlagged };
}

/** Fixed exit-stub length, in canvas units — deliberately NOT tied to any
 * one document's grid/snap setting (the mimic editor's GRID is a per-editor
 * placement convenience, not a document property) so a pipe's exit geometry
 * near an anchored, directional port is identical regardless of snap
 * settings. 12 sits alongside the editor's other fixed on-canvas constants
 * (PORT_SNAP=14, vertex handle radii) rather than deriving from GRID/2. */
export const PORT_STUB = 12;

const DIR_VECTOR: Record<PortDir, [number, number]> = {
	left: [-1, 0],
	right: [1, 0],
	up: [0, -1],
	down: [0, 1]
};

/** Insert a stub segment leaving/entering `points` at its start (or end) in
 * `dir`, PORT_STUB canvas units long — the terminal leg a pipe anchored to a
 * directional port draws before any corner (orthogonal) or straight run
 * toward the next point (direct). A no-op when there's no direction to
 * honor, fewer than 2 points to work with, or the immediately adjacent
 * point already sits exactly at the stub position (two anchored ports close
 * enough together that a redundant stub point would double back on itself). */
function withDirStub(points: [number, number][], dir: PortDir | undefined, atStart: boolean): [number, number][] {
	if (!dir || points.length < 2) return points;
	const [dx, dy] = DIR_VECTOR[dir];
	const anchor = atStart ? points[0] : points[points.length - 1];
	const stub: [number, number] = [anchor[0] + dx * PORT_STUB, anchor[1] + dy * PORT_STUB];
	const next = atStart ? points[1] : points[points.length - 2];
	if (next[0] === stub[0] && next[1] === stub[1]) return points;
	return atStart
		? [points[0], stub, ...points.slice(1)]
		: [...points.slice(0, -1), stub, points[points.length - 1]];
}

/** The points to actually draw for a pipe: anchors resolved to absolute
 * positions (resolvePipeEndpoints, a no-op when neither `from` nor `to` is
 * set — the plain-points pipe every existing document has), exit-direction
 * stubs applied at anchored ends that have one (withDirStub), then routed
 * through orthogonalPoints() when `routing === 'orthogonal'` or passed
 * through as-is otherwise (direct, the default) — the stub is still a
 * straight leg either way, just without a further corner in the 'direct'
 * case. Both the mimic editor (EditorCanvas.svelte) and the runtime
 * (<Mimic>) call this before pointsToPath() so they render identically,
 * PROVIDED they resolve anchors through equivalent `getPort` lookups — see
 * resolveRuntimePorts()'s doc comment for where that can diverge (a
 * sidecar-only override). `getPort` may be omitted for a pipe with no
 * `from`/`to` (the common case, and every pre-anchors document). */
export function routedPoints(
	pipe: Pick<MimicPipe, 'points' | 'routing' | 'from' | 'to'>,
	getPort?: GetPort
): [number, number][] {
	let pts: [number, number][];
	let startDir: PortDir | undefined;
	let endDir: PortDir | undefined;
	if (pipe.from || pipe.to) {
		const resolved = resolvePipeEndpoints(pipe, getPort ?? (() => undefined));
		pts = resolved.points;
		startDir = resolved.startDir;
		endDir = resolved.endDir;
	} else {
		pts = pipe.points;
	}
	pts = withDirStub(pts, startDir, true);
	pts = withDirStub(pts, endDir, false);
	return pipe.routing === 'orthogonal' ? orthogonalPoints(pts) : pts;
}

/** Resolve a binding map (prop -> tag name) against the live tags. A tag
 * name prefixed with "!" negates a boolean. Absent tags resolve to nothing
 * so the component keeps its default — never a fabricated value. */
export function resolveBindings(
	bind: Record<string, string> | undefined,
	tags: Record<string, unknown>
): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	if (!bind) return out;
	for (const [prop, ref] of Object.entries(bind)) {
		const negate = ref.startsWith('!');
		const tag = negate ? ref.slice(1) : ref;
		if (!(tag in tags)) continue;
		const v = tags[tag];
		out[prop] = negate ? v !== true : v;
	}
	return out;
}

// ── attaching free pipe ends to ports ───────────────────────────────────────
// A document that arrives with its pipes as bare polylines — transcribed from
// another HMI, traced off a drawing, auto-generated from a P&ID export — has
// ends that merely COME CLOSE to the equipment they mean. This turns "close"
// into an anchor, the same gesture the mimic editor makes when a dragged pipe
// end snaps to a port (PORT_SNAP), done once over a whole doc as data.

/** One free pipe end the attach pass could not anchor, and the best it found. */
export interface FreePipeEnd {
	pipe: string;
	end: 'from' | 'to';
	x: number;
	y: number;
	/** The nearest resolved port anywhere on the doc, however far — evidence
	 * for choosing a tolerance, or for a tee that is nobody's port. */
	nearest?: { equip: string; port: string; dist: number };
}

export interface AttachReport {
	attached: number;
	free: FreePipeEnd[];
}

export interface AttachOptions {
	/** Farthest a free end may sit from a port and still anchor to it, in
	 * canvas units — one number for every end, or a function of the end for
	 * a caller that knows some ends deserve less reach (a vertex several
	 * pipes share is a junction, and should only anchor when it is ON the
	 * port, not merely near one). */
	tolerance: number | ((end: Pick<FreePipeEnd, 'pipe' | 'end' | 'x' | 'y'>) => number);
	/** The rendered size of an equipment box. `MimicEquipment` carries only
	 * `width` — both renderers measure the height from the DOM, which a pure
	 * function cannot — so a caller that knows better supplies it. Default:
	 * `props.height` when set, else the width (a square). */
	measure?: (eq: MimicEquipment) => { width: number; height: number };
}

function defaultMeasure(eq: MimicEquipment): { width: number; height: number } {
	const width = eq.width ?? 0;
	const h = eq.props?.height;
	return { width, height: typeof h === 'number' ? h : width };
}

interface PortHit {
	equip: string;
	port: string;
	dist: number;
	at: ResolvedPort;
}

/** Anchor every free pipe end that lies within `tolerance` of a port.
 *
 * Pure: returns a new doc, the input is untouched. For each pipe end that is
 * a stored point (no `from`/`to` on that side), the nearest port across all
 * equipment — instance `ports` else BUILTIN_PORTS, via resolveRuntimePorts()
 * — is looked up; within tolerance the stored end point is REPLACED by the
 * anchor (so `points` keeps only interior vertices, per MimicPipe's
 * contract). Both ends of one pipe may attach; a pipe whose two points both
 * attach ends up with `points: []`. Both ends of one pipe are never anchored
 * to the SAME port (that would be a pipe of no length): the closer end takes
 * it and the other is reported free.
 *
 * So the run stays straight, the vertex now adjacent to the anchor is moved
 * onto the port's exit axis when the port has a direction: for `up`/`down`
 * the vertex takes the port's x, for `left`/`right` its y. Only an INTERIOR
 * vertex is moved — an end that stayed free is left where it was (it may be
 * a tee shared with other pipes, and shifting it would open the joint), so
 * that pipe gets its small jog at the free end, where orthogonalPoints()
 * puts it. A vertex that lands on the anchor itself or on the PORT_STUB end
 * is dropped as redundant.
 *
 * Each end is decided against the ORIGINAL geometry, before any vertex is
 * moved, so attaching one end never changes what the other end snaps to. */
export function attachPipeEnds(doc: MimicDoc, opts: AttachOptions): { doc: MimicDoc; report: AttachReport } {
	const measure = opts.measure ?? defaultMeasure;
	const tolerance = (pipe: string, end: 'from' | 'to', pt: [number, number]) =>
		typeof opts.tolerance === 'function' ? opts.tolerance({ pipe, end, x: pt[0], y: pt[1] }) : opts.tolerance;
	// Every resolved port on the canvas, once — through portAbsolute(), the
	// same arithmetic makeGetPort() gives both renderers, so an anchor written
	// here lands where <Mimic> and the editor will draw it.
	const all: { equip: string; port: string; at: ResolvedPort }[] = [];
	for (const eq of doc.equipment ?? []) {
		const { width, height } = measure(eq);
		const box: EquipmentBox = { x: eq.x, y: eq.y, w: width, h: height };
		for (const p of resolveRuntimePorts(eq)) all.push({ equip: eq.id, port: p.name, at: portAbsolute(box, p) });
	}

	const nearest = (x: number, y: number): PortHit | undefined => {
		let best: PortHit | undefined;
		for (const c of all) {
			const dist = Math.hypot(c.at.x - x, c.at.y - y);
			if (!best || dist < best.dist) best = { equip: c.equip, port: c.port, dist, at: c.at };
		}
		return best;
	};

	const report: AttachReport = { attached: 0, free: [] };
	const free = (pipe: string, end: 'from' | 'to', pt: [number, number], hit: PortHit | undefined) =>
		report.free.push({
			pipe,
			end,
			x: pt[0],
			y: pt[1],
			...(hit ? { nearest: { equip: hit.equip, port: hit.port, dist: hit.dist } } : {})
		});

	const pipes = (doc.pipes ?? []).map((pipe): MimicPipe => {
		const pts = pipe.points.map((p) => [p[0], p[1]] as [number, number]);
		// Decide both ends first, against the untouched points.
		const decide = (end: 'from' | 'to'): PortHit | undefined => {
			if (pipe[end]) return undefined; // already anchored
			const pt = end === 'from' ? pts[0] : pts[pts.length - 1];
			if (!pt) return undefined; // nothing stored on that side
			const hit = nearest(pt[0], pt[1]);
			if (hit && hit.dist <= tolerance(pipe.id, end, pt)) return hit;
			free(pipe.id, end, pt, hit);
			return undefined;
		};
		let fromHit = decide('from');
		// A single stored point can serve only one end.
		let toHit = pts.length > 1 || !fromHit ? decide('to') : undefined;
		if (fromHit && toHit && fromHit.equip === toHit.equip && fromHit.port === toHit.port) {
			if (toHit.dist < fromHit.dist) {
				free(pipe.id, 'from', pts[0], fromHit);
				fromHit = undefined;
			} else {
				free(pipe.id, 'to', pts[pts.length - 1], toHit);
				toHit = undefined;
			}
		}
		if (!fromHit && !toHit) return { ...pipe, points: pts };

		let out = pts;
		let from = pipe.from;
		let to = pipe.to;
		if (fromHit) {
			from = { equip: fromHit.equip, port: fromHit.port };
			out = out.slice(1);
			report.attached++;
		}
		if (toHit) {
			to = { equip: toHit.equip, port: toHit.port };
			out = out.slice(0, -1);
			report.attached++;
		}
		// Straighten: the vertex next to each new anchor goes onto its axis —
		// only when that vertex is interior (the other end is anchored too, or
		// there is more than one vertex left between an anchor and a free end).
		const interior = (i: number) => {
			if (i < 0 || i >= out.length) return false;
			if (i === 0 && !from) return false;
			if (i === out.length - 1 && !to) return false;
			return true;
		};
		const straighten = (hit: ResolvedPort, i: number) => {
			if (!interior(i) || !hit.dir) return;
			const v = out[i];
			if (hit.dir === 'up' || hit.dir === 'down') v[0] = hit.x;
			else v[1] = hit.y;
			const [dx, dy] = DIR_VECTOR[hit.dir];
			const onAnchor = v[0] === hit.x && v[1] === hit.y;
			const onStub = v[0] === hit.x + dx * PORT_STUB && v[1] === hit.y + dy * PORT_STUB;
			if (onAnchor || onStub) out.splice(i, 1);
		};
		if (fromHit) straighten(fromHit.at, 0);
		if (toHit) straighten(toHit.at, out.length - 1);

		const next: MimicPipe = { ...pipe, points: out };
		if (from) next.from = from;
		if (to) next.to = to;
		return next;
	});

	return { doc: { ...doc, pipes }, report };
}

// ── piping the nozzles nobody drew a pipe to ─────────────────────────────────
// The complement of attachPipeEnds(): that pass takes pipe ends that come
// close to a port and anchors them; this one takes PORTS that no pipe comes
// close to and runs a pipe from each to the nearest existing run. It is how
// a transcribed screen whose pumps merely sit between two header rails
// (nothing drawn to a flange — the picture implies the connection) becomes a
// doc in which every nozzle is piped.

export interface ConnectOptions {
	/** Farthest a port may be from an existing run and still get a connector,
	 * in canvas units. */
	reach: number;
	/** See AttachOptions.measure. */
	measure?: (eq: MimicEquipment) => { width: number; height: number };
	/** Which of an equipment's ports to pipe, in PRIORITY order — the first
	 * named gets first pick of target runs (see the distinct-target rule).
	 * Default: every port that has an exit direction, in list order.
	 * Undefined from the callback means the default for that equipment. */
	ports?: (eq: MimicEquipment) => string[] | undefined;
}

export interface ConnectedPort {
	equip: string;
	port: string;
	/** The new pipe's id. */
	pipe: string;
	/** The existing pipe the connector runs to. */
	target: string;
	dist: number;
	/** `ray`: the run lay straight ahead in the port's own direction; `nearest`:
	 * it was off-axis, so the connector turns a corner. */
	via: 'ray' | 'nearest';
}

export interface SkippedPort {
	equip: string;
	port: string;
	reason: string;
	nearest?: { pipe: string; dist: number };
}

export interface ConnectReport {
	connected: ConnectedPort[];
	skipped: SkippedPort[];
}

/** Ray from `p` in `d` against segment `a`→`b`: the distance along the ray
 * to the hit, or undefined. A ray that merely grazes a parallel segment does
 * not count — a nozzle sitting ON a run's line is not a nozzle that needs a
 * connector to it. */
function rayHit(
	p: [number, number],
	d: [number, number],
	a: [number, number],
	b: [number, number]
): { t: number; at: [number, number] } | undefined {
	const ex = b[0] - a[0];
	const ey = b[1] - a[1];
	const den = d[0] * ey - d[1] * ex;
	if (Math.abs(den) < 1e-9) return undefined; // parallel
	const wx = a[0] - p[0];
	const wy = a[1] - p[1];
	const t = (wx * ey - wy * ex) / den; // along the ray
	const u = (wx * d[1] - wy * d[0]) / den; // along the segment
	if (t < 0 || u < 0 || u > 1) return undefined;
	return { t, at: [p[0] + d[0] * t, p[1] + d[1] * t] };
}

/** The point of segment `a`→`b` nearest `p`, and how far it is. */
function nearestOn(
	p: [number, number],
	a: [number, number],
	b: [number, number]
): { dist: number; at: [number, number] } {
	const ex = b[0] - a[0];
	const ey = b[1] - a[1];
	const len2 = ex * ex + ey * ey;
	const u = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p[0] - a[0]) * ex + (p[1] - a[1]) * ey) / len2));
	const at: [number, number] = [a[0] + ex * u, a[1] + ey * u];
	return { dist: Math.hypot(at[0] - p[0], at[1] - p[1]), at };
}

/** Run a pipe from every unpiped, directional port to the nearest existing
 * run within `reach`.
 *
 * Pure: returns a new doc. For each equipment, each port the `ports`
 * callback names (default: every port with a resolved direction, in list
 * order) is checked in turn; ports that share one position are ONE nozzle
 * (a `suction`/`in` alias pair gets one connector, under the first name),
 * and a nozzle some pipe already anchors to — under any of its names — is
 * left alone.
 *
 * Target choice, in order:
 *  - a run the port's own ray (its `dir`) hits within reach beats any run
 *    that is merely nearby, so a nozzle facing a header runs straight to it;
 *    otherwise the nearest point on any run within reach, and the connector
 *    turns a corner on the way (orthogonal routing);
 *  - runs that already anchor to THIS equipment are never targets (a pipe
 *    from a pump's discharge is not where its suction goes);
 *  - the ports of one equipment prefer DISTINCT targets: a port only settles
 *    for a run an earlier port of the same equipment chose when no other run
 *    is within reach (a pump with both nozzles on one header is wrong).
 *
 * A port whose exit stub (PORT_STUB in `dir`) would leave the canvas or land
 * inside ANOTHER equipment's box is skipped and reported — the pipe would
 * have to start through a neighbour.
 *
 * The connector is `{from: {equip, port}, points: [hit], routing:
 * 'orthogonal'}` — anchored at the nozzle, its other end a stored vertex ON
 * the target run: a tee, in the same convention a transcribed doc uses for
 * every junction (several pipes ending at one shared point; the kit has no
 * junction object). Ids are `${equip}-${port}`. */
export function connectPorts(doc: MimicDoc, opts: ConnectOptions): { doc: MimicDoc; report: ConnectReport } {
	const measure = opts.measure ?? defaultMeasure;
	const equipment = doc.equipment ?? [];
	const boxes = new Map<string, EquipmentBox>();
	const portsOf = new Map<string, MimicPort[]>();
	for (const eq of equipment) {
		const { width, height } = measure(eq);
		boxes.set(eq.id, { x: eq.x, y: eq.y, w: width, h: height });
		portsOf.set(eq.id, resolveRuntimePorts(eq));
	}
	const getPort = makeGetPort(
		(id) => boxes.get(id),
		(id) => portsOf.get(id)
	);

	const pipes = doc.pipes ?? [];
	const routed = pipes.map((p) => ({ pipe: p, pts: routedPoints(p, getPort) }));
	const key = (x: number, y: number) => `${x},${y}`;

	// Which nozzle POSITIONS on each equipment already have a pipe.
	const anchored = new Map<string, Set<string>>();
	const anchorsTo = new Map<string, Set<string>>(); // pipe id -> equipment ids
	for (const p of pipes) {
		for (const a of [p.from, p.to]) {
			if (!a) continue;
			const r = getPort(a.equip, a.port);
			if (!r) continue;
			if (!anchored.has(a.equip)) anchored.set(a.equip, new Set());
			anchored.get(a.equip)!.add(key(r.x, r.y));
			if (!anchorsTo.has(p.id)) anchorsTo.set(p.id, new Set());
			anchorsTo.get(p.id)!.add(a.equip);
		}
	}

	const inBox = (b: EquipmentBox, x: number, y: number) => x > b.x && x < b.x + b.w && y > b.y && y < b.y + b.h;
	const report: ConnectReport = { connected: [], skipped: [] };
	const added: MimicPipe[] = [];

	for (const eq of equipment) {
		const ports = portsOf.get(eq.id) ?? [];
		const names = opts.ports?.(eq) ?? ports.filter((p) => resolvedPortDir(p)).map((p) => p.name);
		const done = new Set(anchored.get(eq.id) ?? []);
		const used = new Set<string>();
		const box = boxes.get(eq.id)!;
		for (const name of names) {
			const port = ports.find((p) => p.name === name);
			if (!port) {
				report.skipped.push({ equip: eq.id, port: name, reason: 'no such port' });
				continue;
			}
			const at = portAbsolute(box, port);
			const k = key(at.x, at.y);
			if (done.has(k)) continue; // piped already, or an alias of one just piped
			done.add(k);
			if (!at.dir) {
				report.skipped.push({ equip: eq.id, port: name, reason: 'no exit direction' });
				continue;
			}
			const [dx, dy] = DIR_VECTOR[at.dir];
			const p: [number, number] = [at.x, at.y];
			const stub: [number, number] = [at.x + dx * PORT_STUB, at.y + dy * PORT_STUB];
			if (stub[0] < 0 || stub[1] < 0 || stub[0] > doc.canvas.width || stub[1] > doc.canvas.height) {
				report.skipped.push({ equip: eq.id, port: name, reason: 'stub leaves the canvas' });
				continue;
			}
			const blocker = equipment.find(
				(o) => o.id !== eq.id && !inBox(boxes.get(o.id)!, at.x, at.y) && inBox(boxes.get(o.id)!, stub[0], stub[1])
			);
			if (blocker) {
				report.skipped.push({ equip: eq.id, port: name, reason: `stub lands inside ${blocker.id}` });
				continue;
			}

			// Best target among a set of runs: ray first, then nearest.
			const search = (skipUsed: boolean) => {
				let ray: { pipe: string; t: number; at: [number, number] } | undefined;
				let near: { pipe: string; dist: number; at: [number, number] } | undefined;
				for (const { pipe, pts } of routed) {
					if (anchorsTo.get(pipe.id)?.has(eq.id)) continue;
					if (skipUsed && used.has(pipe.id)) continue;
					for (let i = 1; i < pts.length; i++) {
						const h = rayHit(p, [dx, dy], pts[i - 1], pts[i]);
						if (h && h.t <= opts.reach && (!ray || h.t < ray.t)) ray = { pipe: pipe.id, ...h };
						const n = nearestOn(p, pts[i - 1], pts[i]);
						if (!near || n.dist < near.dist) near = { pipe: pipe.id, ...n };
					}
				}
				return { ray, near };
			};
			let { ray, near } = search(true);
			if (!ray && !(near && near.dist <= opts.reach)) ({ ray, near } = search(false));
			const hit = ray
				? { pipe: ray.pipe, dist: ray.t, at: ray.at, via: 'ray' as const }
				: near && near.dist <= opts.reach
					? { pipe: near.pipe, dist: near.dist, at: near.at, via: 'nearest' as const }
					: undefined;
			if (!hit) {
				report.skipped.push({
					equip: eq.id,
					port: name,
					reason: 'no run within reach',
					...(near ? { nearest: { pipe: near.pipe, dist: near.dist } } : {})
				});
				continue;
			}
			const id = `${eq.id}-${name}`;
			added.push({ id, from: { equip: eq.id, port: name }, points: [hit.at], routing: 'orthogonal' });
			used.add(hit.pipe);
			report.connected.push({ equip: eq.id, port: name, pipe: id, target: hit.pipe, dist: hit.dist, via: hit.via });
		}
	}

	return { doc: { ...doc, pipes: [...pipes, ...added] }, report };
}
