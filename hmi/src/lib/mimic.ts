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
 * `rate`) to tags to animate flow with the process. */
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
