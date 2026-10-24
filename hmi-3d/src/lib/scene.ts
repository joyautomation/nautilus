// The scene document: a 3D process view as DATA, the spatial sibling of the
// HMI kit's mimic document (hmi/src/lib/mimic.ts). A *.scene.json is to the
// 3D view what a *.mimic.json is to <Mimic>: nodes placed in space, pipes
// run between them, live tag bindings — rendered by one component, edited
// as text, diffed and reviewed like the program it belongs to.
//
// Positions are METRES in the site frame: the origin marker at [0, 0, 0],
// +y up, the same frame the rig's assets.yaml surveys. The file that drives
// the desktop view is therefore also the file that places AR overlays.
// Brief: docs/design/spatial-hmi.md.

import { validateDrives, validStatusTemplate, driveMembers, statusMembers, type Drive } from './drives.js';

export type Vec3 = [number, number, number];

export interface SceneCamera {
	pos: Vec3;
	target: Vec3;
	/** Vertical field of view, degrees. Default 45. Perspective only. */
	fov?: number;
	/** `perspective` (default), or `orthographic`: no vanishing point, so a
	 * pos on a 45° azimuth and a 35.26° elevation is the isometric drawing. */
	projection?: 'perspective' | 'orthographic';
	/** Orthographic only: how many metres of scene span the view's width.
	 * Default 6. */
	width?: number;
}

/** How the scene is drawn (docs/design/spatial-hmi.md §3f). `lit` (default):
 * data kinds, PBR, textures and the environment. `flat`: grey primitives
 * under three lights. `paper`: the drawn look — near-white fills, every
 * edge inked, a paper backdrop, and only an alarm brings colour. */
export type SceneLook = 'lit' | 'flat' | 'paper';

/** Static, unbound geometry: the desk, a floor slab, the origin marker. */
export interface SceneFixture {
	kind: 'box' | 'plane' | 'marker';
	pos: Vec3;
	/** box: [w, h, d]; plane / marker: [w, d]. */
	size?: Vec3 | [number, number];
	/** Degrees, XYZ order. */
	rot?: Vec3;
	color?: string;
	opacity?: number;
	/** plane only: PBR maps, so a floor is concrete rather than a colour.
	 * Paths are URL paths from the app root (`textures/floor_diff.jpg`). */
	texture?: SceneTexture;
}

export interface SceneTexture {
	map: string;
	normalMap?: string;
	roughnessMap?: string;
	/** Tiles across [w, d]. Default [1, 1]. */
	repeat?: [number, number];
}

/** The surroundings (docs/design/spatial-hmi.md §3c): an HDRI for
 * image-based lighting, optionally as the backdrop, and shadows. Absent =
 * the flat look: three lights, no environment. */
export interface SceneEnvironment {
	/** An equirectangular .hdr or .exr, as a URL path from the app root. */
	hdri?: string;
	/** What you SEE when `background` is on: a larger equirect image (a
	 * tonemapped .jpg/.png, or another .hdr/.exr) in place of the HDRI, so
	 * lighting can come from a small file and the backdrop from a sharp one. */
	backdrop?: string;
	/** `none` lights only (default); `sky` shows the backdrop (or the HDRI);
	 * `ground` projects it onto a floor at `floor` so a desk-scale scene
	 * stands in it. */
	background?: 'none' | 'sky' | 'ground';
	/** Distance fade toward a colour: the far backdrop and the projected
	 * floor soften deliberately instead of reading as a low-res photo. */
	fog?: { color?: string; near?: number; far?: number };
	/** The floor's y for `ground`, metres. Default 0. */
	floor?: number;
	/** Environment light multiplier. Default 1. */
	intensity?: number;
	/** A shadow-casting key light with soft shadows (desktop tier). */
	shadows?: boolean;
}

/** A reference grid on a horizontal plane. */
export interface SceneGrid {
	pos?: Vec3;
	/** [w, d] in metres. Default [4, 4]. */
	size?: [number, number];
	/** Cell pitch, metres. Default 0.1. */
	cell?: number;
	/** Heavier line every this many metres. Default 1. */
	section?: number;
}

export interface SceneNode {
	/** Unique in the document; what a pick reports. */
	id: string;
	/** Registry key: 'tank' | 'pump' | 'valve' | one the app registered. */
	kind: string;
	/** The struct tag this node binds; the kind's component reads its members. */
	tag?: string;
	/** Display name; defaults to id. */
	label?: string;
	/** Metres, site frame. */
	pos: Vec3;
	/** Degrees, XYZ order; [0, yaw, 0] turns the node on the spot. */
	rot?: Vec3;
	scale?: number;
	/** Static props passed straight through to the component. */
	props?: Record<string, unknown>;
	/** Live props: component prop -> ref. Same map and rules as a mimic
	 * equipment's `bind`, plus dotted paths — see bindings.ts. */
	bind?: Record<string, string>;
}

/** A box in a kind's frame: the kind's bounds, or one named part's. */
export interface SceneBox {
	size: Vec3;
	center: Vec3;
}

/** A point named by where it sits on a box (§3g, slice 3): `anchor` is a
 * face or corner word — `top`, `bottom`, `left` (−x), `right` (+x),
 * `front` (+z), `back` (−z), `center`, or two joined (`top-front`) — on
 * the named `part`'s box, or on the kind's own bounds when `part` is
 * absent; `offset` nudges in metres. It survives the model being
 * re-exported and reads better than three numbers. */
export interface BoxPoint {
	part?: string;
	anchor: string;
	offset?: Vec3;
}

/** A point in a kind's frame: metres, or a place on a box. */
export type ScenePoint = Vec3 | BoxPoint;

/** A port on a kind (§3g, slice 3): where a pipe meets it, in the kind's
 * own frame, and the direction a pipe leaves it (unit vector; default:
 * out of the anchored face, else up). */
export interface ScenePort {
	at: ScenePoint;
	dir?: Vec3;
}

export const ANCHOR_WORDS = ['center', 'top', 'bottom', 'left', 'right', 'front', 'back'] as const;
const AXIS_OF: Record<string, 0 | 1 | 2> = { left: 0, right: 0, bottom: 1, top: 1, back: 2, front: 2 };
const SIGN_OF: Record<string, -1 | 1> = { left: -1, right: 1, bottom: -1, top: 1, back: -1, front: 1 };

/** `top`, `front`, `top-front`, `center`: one or two face words, at most
 * one per axis, in any order. */
export function isAnchorWord(s: unknown): s is string {
	if (typeof s !== 'string' || !s) return false;
	const parts = s.split('-');
	if (parts.length > 2 || !parts.every((w) => (ANCHOR_WORDS as readonly string[]).includes(w))) return false;
	if (parts.length === 2 && (parts.includes('center') || AXIS_OF[parts[0]] === AXIS_OF[parts[1]])) return false;
	return true;
}

/** The point an anchor word names on a box. */
export function anchorPoint(box: SceneBox, anchor: string): Vec3 {
	const p: Vec3 = [...box.center] as Vec3;
	for (const w of anchor.split('-')) {
		if (w === 'center') continue;
		const a = AXIS_OF[w];
		p[a] = box.center[a] + (SIGN_OF[w] * box.size[a]) / 2;
	}
	return p;
}

/** The outward direction of an anchor: the face's normal (a corner's is
 * the sum, unit); `center` is up. */
export function anchorNormal(anchor: string): Vec3 {
	const d: Vec3 = [0, 0, 0];
	for (const w of anchor.split('-')) if (w !== 'center') d[AXIS_OF[w]] += SIGN_OF[w];
	const len = Math.hypot(...d);
	return len ? ([d[0] / len, d[1] / len, d[2] / len] as Vec3) : [0, 1, 0];
}

/** The boxes a kind's points resolve against: its bounds and, once the
 * model is loaded, each named part's box, all in the kind's frame. */
export type KindBoxes = { bounds?: SceneBox; parts?: Record<string, SceneBox> };

export function isBoxPoint(v: unknown): v is BoxPoint {
	return isObj(v) && isAnchorWord(v.anchor) && (v.part === undefined || isStr(v.part)) && (v.offset === undefined || isVec3(v.offset));
}

/** A point in the kind's frame, from metres, a box point, or a bare
 * anchor word (on the bounds). Null when the box it names is not known
 * yet (the model has not loaded) or does not exist. */
export function resolvePoint(p: ScenePoint | string | undefined, boxes: KindBoxes | undefined): Vec3 | null {
	if (p === undefined) return null;
	if (isVec3(p)) return [p[0], p[1], p[2]];
	const bp: BoxPoint = typeof p === 'string' ? { anchor: p } : p;
	const box = bp.part !== undefined ? boxes?.parts?.[bp.part] : boxes?.bounds;
	if (!box) return null;
	const at = anchorPoint(box, bp.anchor);
	if (bp.offset) for (let i = 0; i < 3; i++) at[i] += bp.offset[i];
	return at;
}

/** A port's point and direction in the kind's frame; null until its box is known. */
export function resolvePort(port: ScenePort, boxes: KindBoxes | undefined): { at: Vec3; dir: Vec3 } | null {
	const at = resolvePoint(port.at, boxes);
	if (!at) return null;
	const dir = port.dir ?? (isVec3(port.at) ? [0, 1, 0] : anchorNormal(port.at.anchor));
	const len = Math.hypot(...dir) || 1;
	return { at, dir: [dir[0] / len, dir[1] / len, dir[2] / len] };
}

/** A pipe end anchored to a node's port: resolved through the node's
 * `pos` / `rot` / `scale` by the renderer and `naut check` alike, so the
 * pipe follows the node when it is moved. */
export interface PipeAnchor {
	node: string;
	port: string;
}

export interface ScenePipe {
	id?: string;
	/** The ends, when anchored to ports; `points` are then the run between
	 * them (may be empty: a straight pipe port to port). */
	from?: PipeAnchor;
	to?: PipeAnchor;
	/** Polyline in scene metres. With both ends anchored it may be absent;
	 * the run needs at least two points counting the anchored ends. */
	points?: Vec3[];
	/** Metres. Default 0.012. */
	radius?: number;
	/** `flowing` is the one live prop a pipe has, the same key the mimic's
	 * pipe binds. Booleans as-is; a number is flowing above 2 (%). */
	bind?: { flowing?: string };
	/** Motion along the run while it flows (default true): a train of
	 * bulges in the lit and flat looks, of chevrons on paper, so direction
	 * reads even where colour cannot. The run's direction is the order of
	 * its points. */
	arrows?: boolean;
}

/** An assembly (docs/design/spatial-hmi.md §3d): a kind made of kinds.
 * Parts are nodes in the kind's own frame, and a part's `tag` is a MEMBER
 * of the assembly's struct (`Pump` inside `SK101` reads `SK101.Pump`); a
 * ref inside (a part's `bind`, a pipe's `flowing`) is a path from that
 * struct. That nesting is the whole parameter mechanism. */
export interface SceneAssembly {
	nodes: SceneNode[];
	pipes?: ScenePipe[];
}

/** The kind ↔ struct-type contract for a kind (docs/design/spatial-hmi.md
 * §3b). The built-in kinds carry defaults; a project re-points one with
 * `type`, and declares its own kinds with `type` and the `members` their
 * component reads, so `naut check` can hold every node to it. A kind is
 * then defined ONE of three ways, or by the app's registry: a `model`
 * (§3c), a `component` file or an `assembly` (§3d). */
export interface SceneKind {
	/** The UDT a node of this kind binds. */
	type?: string;
	/** The members the kind's component reads off the struct. */
	members?: string[];
	/** A data kind (§3c): a glTF/GLB, as a URL path from the app root.
	 * Absent = the registry's Svelte component of this name. */
	model?: string;
	/** A component kind (§3d): a Svelte file, as a source path relative to
	 * the scene file (`hmi/src/lib/Skid.svelte`). The app builds it and
	 * hands SceneView its modules; the extension lists it and `naut check`
	 * holds its nodes to `type`/`members`. */
	component?: string;
	/** An assembly (§3d): parts and pipes in the kind's own frame. */
	assembly?: SceneAssembly;
	/** The halo / selection box: the loaded model's box, or an explicit one. */
	bounds?: 'auto' | { size: Vec3; center: Vec3 };
	/** Where the label floats: metres, a box point, or a bare anchor word on
	 * the bounds. Default: `top`. */
	labelAt?: ScenePoint | string;
	/** The label's value text as a template: `{Level:1} %`, `{Running?run:stopped}`. */
	status?: string;
	/** Where pipes meet this kind, in its own frame (§3g, slice 3). */
	ports?: Record<string, ScenePort>;
	/** What live values do to named meshes in the model. */
	drive?: Drive[];
}

/** A `kinds` entry: the contract inline, or the path of a `{Name}.kind.json`
 * holding the same keys — a kind in a file of its own, reused across
 * scenes and opened in its own editor (§3g slice 2). The path is relative
 * to the scene file, like a component's (`hmi/src/lib/kinds/pump.kind.json`),
 * and the app hands SceneView the file through `modules` the same way. */
export type SceneKindEntry = SceneKind | string;

/** The members an assembly reads off its struct: every part's `tag`, and
 * the root of every ref inside it, deduplicated, in order (§3d). */
export function assemblyMembers(a: SceneAssembly | undefined): string[] {
	const out: string[] = [];
	const add = (ref: string | undefined) => {
		if (!ref) return;
		const m = refRoot(ref);
		if (m && !out.includes(m)) out.push(m);
	};
	for (const n of a?.nodes ?? []) {
		add(n.tag);
		for (const ref of Object.values(n.bind ?? {})) add(ref);
	}
	for (const p of a?.pipes ?? []) add(p.bind?.flowing);
	return out;
}

/** Every member a kind reads: `members`, plus what its drives, its status
 * template and its assembly name — so nothing repeats the list (§3c, §3d). */
export function kindMembers(k: SceneKindEntry | undefined): string[] {
	if (typeof k === 'string') return [];
	const out = [...(k?.members ?? [])];
	for (const m of [...driveMembers(k?.drive), ...statusMembers(k?.status), ...assemblyMembers(k?.assembly)])
		if (!out.includes(m)) out.push(m);
	return out;
}

/** How a `kinds` entry defines its kind, if it does. `file` is a path
 * entry: what the file defines is known once it is inlined (defs.ts
 * `inlineKindFiles`). */
export function kindDefinedBy(k: SceneKindEntry | undefined): 'model' | 'component' | 'assembly' | 'file' | undefined {
	if (!k) return undefined;
	if (typeof k === 'string') return 'file';
	if (k.model !== undefined) return 'model';
	if (k.component !== undefined) return 'component';
	if (k.assembly !== undefined) return 'assembly';
	return undefined;
}

export interface SceneDoc {
	name?: string;
	/** The look the document asks for; the page may override it. Default `lit`. */
	look?: SceneLook;
	/** Kind contracts: overrides for the built-ins, declarations for the app's
	 * own, data kinds — inline, or as the path of a kind file. */
	kinds?: Record<string, SceneKindEntry>;
	environment?: SceneEnvironment;
	camera?: SceneCamera;
	fixtures?: SceneFixture[];
	grid?: SceneGrid;
	nodes: SceneNode[];
	pipes?: ScenePipe[];
	/** Reserved: tags an operator may write from this scene. The view is
	 * read-only until the write path ships, so a non-empty list is an error
	 * rather than a silent no-op. */
	writable?: string[];
}

/** The ports a document's own `kinds` entry gives a kind, if it is inline
 * and has any; undefined otherwise (a file not yet inlined, the app's kind). */
export function docPorts(doc: { kinds?: Record<string, SceneKindEntry> } | null | undefined, kind: string): Record<string, ScenePort> | undefined {
	const k = doc?.kinds?.[kind];
	return k && typeof k !== 'string' ? k.ports : undefined;
}

const DEG = Math.PI / 180;
/** `v` turned by a node's `rot` (degrees, XYZ order — three's Euler): about
 * z, then y, then x, the way Rx·Ry·Rz applies. */
export function rotateXYZ(v: Vec3, rot: Vec3 | undefined): Vec3 {
	if (!rot) return [v[0], v[1], v[2]];
	let [x, y, z] = v;
	const [ax, ay, az] = [rot[0] * DEG, rot[1] * DEG, rot[2] * DEG];
	// z
	[x, y] = [x * Math.cos(az) - y * Math.sin(az), x * Math.sin(az) + y * Math.cos(az)];
	// y
	[x, z] = [x * Math.cos(ay) + z * Math.sin(ay), -x * Math.sin(ay) + z * Math.cos(ay)];
	// x
	[y, z] = [y * Math.cos(ax) - z * Math.sin(ax), y * Math.sin(ax) + z * Math.cos(ax)];
	return [x, y, z];
}

/** A port in the frame the node sits in: its point through the node's
 * pos / rot / scale, its direction through rot alone (unit). */
export function portInFrame(node: Pick<SceneNode, 'pos' | 'rot' | 'scale'>, port: ScenePort, boxes?: KindBoxes): { at: Vec3; dir: Vec3 } | null {
	const local = resolvePort(port, boxes);
	if (!local) return null;
	const s = node.scale ?? 1;
	const r = rotateXYZ([local.at[0] * s, local.at[1] * s, local.at[2] * s], node.rot);
	const at: Vec3 = [node.pos[0] + r[0], node.pos[1] + r[1], node.pos[2] + r[2]];
	const d = rotateXYZ(local.dir, node.rot);
	const len = Math.hypot(d[0], d[1], d[2]) || 1;
	return { at, dir: [d[0] / len, d[1] / len, d[2] / len] };
}

/** A pipe's run as points: an anchored end resolved through its node and
 * port, the `points` between. Null when an anchor names no node, a port
 * its kind does not have, or a port on a box not yet known (the model
 * still loading) — the pipe cannot be drawn yet. */
export function resolvePipePoints(
	pipe: Pick<ScenePipe, 'from' | 'to' | 'points'>,
	nodes: readonly SceneNode[],
	portsOf: (kind: string) => Record<string, ScenePort> | undefined,
	boxesOf?: (nodeId: string) => KindBoxes | undefined
): Vec3[] | null {
	const end = (a: PipeAnchor | undefined): Vec3 | null | undefined => {
		if (!a) return undefined;
		const n = nodes.find((x) => x.id === a.node);
		const port = n && portsOf(n.kind)?.[a.port];
		return n && port ? (portInFrame(n, port, boxesOf?.(n.id))?.at ?? null) : null;
	};
	const from = end(pipe.from);
	const to = end(pipe.to);
	if (from === null || to === null) return null;
	const out: Vec3[] = [];
	if (from) out.push(from);
	out.push(...(pipe.points ?? []));
	if (to) out.push(to);
	return out.length >= 2 ? out : null;
}

export interface SceneError {
	/** Where in the document, JSON-pointer style: `/nodes/2/pos`. */
	path: string;
	message: string;
}

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
const isVec3 = (v: unknown): v is Vec3 => Array.isArray(v) && v.length === 3 && v.every(isNum);
const isVec2 = (v: unknown): v is [number, number] => Array.isArray(v) && v.length === 2 && v.every(isNum);
const isStr = (v: unknown): v is string => typeof v === 'string' && v.length > 0;
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);

/** An asset path: a URL path from the app root, no scheme, no `..`. */
export function isAssetPath(p: unknown): p is string {
	return isStr(p) && !/^[a-z]+:/i.test(p) && !p.split('/').includes('..');
}

/** A component path (§3d): a source path relative to the scene file, a
 * `.svelte` file, no scheme, no `..`, not absolute. */
export function isComponentPath(p: unknown): p is string {
	return isAssetPath(p) && p.endsWith('.svelte') && !p.startsWith('/');
}

/** A kind file path (§3g): a `{Name}.kind.json`, relative to the scene
 * file like a component's — no scheme, no `..`, not absolute. */
export function isKindPath(p: unknown): p is string {
	return isAssetPath(p) && p.endsWith('.kind.json') && !p.startsWith('/');
}

const MEMBER_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** A part's tag inside an assembly: one member name of the assembly's struct. */
export function isMemberName(s: unknown): s is string {
	return isStr(s) && MEMBER_RE.test(s);
}

/** Is `ref` a well-formed binding ref? `!` may lead; then a dotted path of
 * non-empty segments. (The grammar is the mimic's plus dots; bindings.ts.) */
export function isBindingRef(ref: unknown): boolean {
	if (!isStr(ref)) return false;
	const path = ref.startsWith('!') ? ref.slice(1) : ref;
	return path.length > 0 && path.split('.').every((s) => s.length > 0);
}

type Err = (path: string, message: string) => void;

export const LOOKS: readonly SceneLook[] = ['lit', 'flat', 'paper'];

/** One node's structural rules — a document node or an assembly's part. */
function checkNode(n: unknown, p: string, err: Err, known: Set<string> | null, ids: Set<string>): void {
	if (!isObj(n)) return err(p, 'must be an object');
	if (!isStr(n.id)) err(`${p}/id`, 'must be a non-empty string');
	else if (ids.has(n.id)) err(`${p}/id`, `duplicate id "${n.id}"`);
	else ids.add(n.id);
	if (!isStr(n.kind)) err(`${p}/kind`, 'must be a non-empty string');
	else if (known && !known.has(n.kind)) err(`${p}/kind`, `unknown kind "${n.kind}" (registered: ${[...known].join(', ')})`);
	if (n.tag !== undefined && !isStr(n.tag)) err(`${p}/tag`, 'must be a non-empty tag name');
	if (n.label !== undefined && typeof n.label !== 'string') err(`${p}/label`, 'must be a string');
	if (!isVec3(n.pos)) err(`${p}/pos`, 'must be [x, y, z] metres');
	if (n.rot !== undefined && !isVec3(n.rot)) err(`${p}/rot`, 'must be [x, y, z] degrees');
	if (n.scale !== undefined && !(isNum(n.scale) && n.scale > 0)) err(`${p}/scale`, 'must be > 0');
	if (n.props !== undefined && !isObj(n.props)) err(`${p}/props`, 'must be an object');
	if (n.bind !== undefined) {
		if (!isObj(n.bind)) err(`${p}/bind`, 'must be an object of prop -> ref');
		else
			for (const [prop, ref] of Object.entries(n.bind))
				if (!isBindingRef(ref)) err(`${p}/bind/${prop}`, `"${String(ref)}" is not a tag ref (Tag, Tag.Member, !Tag)`);
	}
}

/** The frame a pipe's anchors resolve in: the nodes beside it (the
 * document's, or an assembly's) and what ports each node's kind has, when
 * known (undefined = the kind is the app's or a file not yet inlined: the
 * port is taken on trust). */
export type PipeFrame = { nodes: unknown; portsOf: (kind: string) => Record<string, ScenePort> | undefined };

const PORT_RE = /^[A-Za-z_][A-Za-z0-9_-]*$/;

function checkAnchor(a: unknown, p: string, err: Err, frame: PipeFrame | undefined): void {
	if (!isObj(a)) return err(p, 'must be { node, port }');
	if (!isStr(a.node)) return err(`${p}/node`, 'must name a node');
	if (!isStr(a.port)) return err(`${p}/port`, "must name one of the node's kind's ports");
	if (!frame || !Array.isArray(frame.nodes)) return;
	const n = frame.nodes.find((x) => isObj(x) && x.id === a.node) as Record<string, unknown> | undefined;
	if (!n) return err(`${p}/node`, `no node "${a.node}" in this frame`);
	const ports = isStr(n.kind) ? frame.portsOf(n.kind) : undefined;
	if (ports && !(a.port in ports))
		err(`${p}/port`, `kind "${String(n.kind)}" has no port "${a.port}" (it has ${Object.keys(ports).join(', ') || 'none'})`);
}

function checkPipe(pipe: unknown, p: string, err: Err, frame?: PipeFrame): void {
	if (!isObj(pipe)) return err(p, 'must be an object');
	let ends = 0;
	for (const k of ['from', 'to'] as const)
		if (pipe[k] !== undefined) {
			ends++;
			checkAnchor(pipe[k], `${p}/${k}`, err, frame);
		}
	if (pipe.points !== undefined && !Array.isArray(pipe.points)) err(`${p}/points`, 'must be an array of [x, y, z]');
	else {
		const pts = pipe.points ?? [];
		if (pts.length + ends < 2) err(`${p}/points`, ends ? 'needs at least two points counting the anchored ends' : 'needs at least two points');
		pts.forEach((pt, j) => isVec3(pt) || err(`${p}/points/${j}`, 'must be [x, y, z] metres'));
	}
	if (pipe.radius !== undefined && !(isNum(pipe.radius) && pipe.radius > 0)) err(`${p}/radius`, 'must be > 0');
	if (pipe.arrows !== undefined && typeof pipe.arrows !== 'boolean') err(`${p}/arrows`, 'must be true or false');
	if (pipe.bind !== undefined) {
		if (!isObj(pipe.bind)) err(`${p}/bind`, 'must be an object');
		else {
			for (const k of Object.keys(pipe.bind)) if (k !== 'flowing') err(`${p}/bind/${k}`, "a pipe binds only 'flowing'");
			if (pipe.bind.flowing !== undefined && !isBindingRef(pipe.bind.flowing))
				err(`${p}/bind/flowing`, `"${String(pipe.bind.flowing)}" is not a tag ref`);
		}
	}
}

/** The checks one kind's definition gets, inline or in its own file:
 * paths, one way of being defined, bounds, label, status, drives. `p` is
 * the JSON-pointer prefix the messages carry. */
function checkKindDef(def: Record<string, unknown>, p: string, known: Set<string> | null, assemblies: Set<string>): SceneError[] {
	const errors: SceneError[] = [];
	const err = (path: string, message: string) => errors.push({ path, message });
	if (def.type !== undefined && !isStr(def.type)) err(`${p}/type`, 'must be a UDT name');
	if (def.members !== undefined && !(Array.isArray(def.members) && def.members.every(isStr)))
		err(`${p}/members`, 'must be an array of member names');
	if (def.ports !== undefined) {
		if (!isObj(def.ports)) err(`${p}/ports`, 'must be an object of port name -> { at, dir? }');
		else
			for (const [name, port] of Object.entries(def.ports)) {
				const pp = `${p}/ports/${name}`;
				if (!PORT_RE.test(name)) err(pp, 'a port name is letters, digits, _ or -');
				if (!isObj(port)) err(pp, 'must be { at: [x, y, z] | { part?, anchor, offset? }, dir?: [x, y, z] }');
				else {
					if (!(isVec3(port.at) || isBoxPoint(port.at)))
						err(`${pp}/at`, "must be [x, y, z] metres in the kind's frame, or { part?, anchor: top | bottom | left | right | front | back | center (or two, top-front), offset? }");
					if (port.dir !== undefined && !(isVec3(port.dir) && port.dir.some((c) => c !== 0))) err(`${pp}/dir`, 'must be a non-zero [x, y, z] direction');
				}
			}
	}
	if (def.model !== undefined && !(isAssetPath(def.model) && /\.(glb|gltf)$/i.test(def.model)))
		err(`${p}/model`, 'must be a URL path to a .glb or .gltf (models/pump.glb)');
	if (def.component !== undefined && !isComponentPath(def.component))
		err(`${p}/component`, 'must be a .svelte file as a path relative to the scene file (hmi/src/lib/Skid.svelte)');
	const ways = ['model', 'component', 'assembly'].filter((w) => def[w] !== undefined);
	if (ways.length > 1) err(p, `a kind is defined one way, not ${ways.join(' and ')}`);
	if (def.assembly !== undefined) {
		const ap = `${p}/assembly`;
		if (!isObj(def.assembly) || !Array.isArray(def.assembly.nodes)) err(ap, 'must be { nodes: [...], pipes?: [...] }');
		else {
			const ids = new Set<string>();
			def.assembly.nodes.forEach((n, i) => {
				checkNode(n, `${ap}/nodes/${i}`, err, known, ids);
				if (!isObj(n)) return;
				if (n.tag !== undefined && !isMemberName(n.tag)) err(`${ap}/nodes/${i}/tag`, "a part's tag is one member of the assembly's struct (Pump)");
				if (isStr(n.kind) && assemblies.has(n.kind)) err(`${ap}/nodes/${i}/kind`, `"${n.kind}" is an assembly; a part is a built-in, a data kind or a component kind`);
			});
			if (def.assembly.pipes !== undefined) {
				if (!Array.isArray(def.assembly.pipes)) err(`${ap}/pipes`, 'must be an array');
				else {
					// Anchors inside an assembly resolve among its parts; a part's
					// kind is one the document declares, or the app's.
					const frame: PipeFrame = { nodes: def.assembly.nodes, portsOf: () => undefined };
					def.assembly.pipes.forEach((pipe, i) => checkPipe(pipe, `${ap}/pipes/${i}`, err, frame));
				}
			}
		}
	}
	if (def.bounds !== undefined && def.bounds !== 'auto') {
		if (!isObj(def.bounds) || !isVec3(def.bounds.size) || !isVec3(def.bounds.center))
			err(`${p}/bounds`, "must be 'auto' or { size: [w, h, d], center: [x, y, z] }");
	}
	if (def.labelAt !== undefined && !(isVec3(def.labelAt) || isAnchorWord(def.labelAt) || isBoxPoint(def.labelAt)))
		err(`${p}/labelAt`, 'must be [x, y, z] metres, an anchor word (top, top-front, …) on the bounds, or { part?, anchor, offset? }');
	if (def.status !== undefined && !validStatusTemplate(def.status))
		err(`${p}/status`, 'must be a template over members: "{Level:1} %", "{Running?run:stopped}"');
	if (def.drive !== undefined) errors.push(...validateDrives(def.drive, `${p}/drive`));
	if (def.model === undefined && def.drive !== undefined) err(`${p}/drive`, 'drives need a model — without one the Svelte kind of this name renders');
	if (ways.length === 0 && (def.bounds !== undefined || def.labelAt !== undefined))
		err(p, 'bounds and labelAt need a model, a component or an assembly — without one the registry\'s kind of this name renders');
	return errors;
}

/** Validate a kind file's content on its own (the kind editor, a test):
 * the same rules a `kinds` entry gets, with paths from the file's root. A
 * `component` or an `assembly` is refused — a kind file is a data kind or
 * a bare contract; those two stay inline, where the scene can see them. */
export function validateKind(def: unknown): { ok: boolean; errors: SceneError[] } {
	if (!isObj(def)) return { ok: false, errors: [{ path: '', message: 'a kind file is an object: { model, status, drive, ... }' }] };
	const errors = checkKindDef(def, '', null, new Set());
	for (const w of ['component', 'assembly'] as const)
		if (def[w] !== undefined) errors.push({ path: `/${w}`, message: `a kind file is a data kind (model + drives) or a contract — a ${w} kind stays inline in the scene` });
	return { ok: errors.length === 0, errors };
}

/**
 * Validate a scene document structurally — the same job `naut check` does
 * for a manifest, so a typo in a scene file is a list of errors with paths
 * rather than a blank canvas. `kinds`, when given, is the set of registered
 * node kinds (a registry's keys); an unknown kind is an error only then.
 *
 * Pure. Accepts `unknown` so a freshly parsed JSON file can be checked
 * before anything trusts its shape.
 */
export function validateScene(doc: unknown, kinds?: Iterable<string>): { ok: boolean; errors: SceneError[] } {
	const errors: SceneError[] = [];
	const err = (path: string, message: string) => errors.push({ path, message });
	const known = kinds ? new Set(kinds) : null;
	// A kind the document defines — a model, a component file or an
	// assembly — is a kind the document brings (§3c, §3d).
	const assemblies = new Set<string>();
	if (isObj(doc) && isObj(doc.kinds))
		for (const [k, def] of Object.entries(doc.kinds)) {
			// A kind file defines its kind too; what it holds is checked once
			// it is inlined (SceneView does that from the app's modules).
			if (isKindPath(def)) known?.add(k);
			if (!isObj(def)) continue;
			if (def.model !== undefined || def.component !== undefined || def.assembly !== undefined) known?.add(k);
			if (def.assembly !== undefined) assemblies.add(k);
		}

	if (!isObj(doc)) return { ok: false, errors: [{ path: '', message: 'scene must be an object' }] };

	if (doc.look !== undefined && !LOOKS.includes(doc.look as SceneLook)) err('/look', "must be 'lit', 'flat' or 'paper'");

	if (doc.camera !== undefined) {
		if (!isObj(doc.camera)) err('/camera', 'must be an object');
		else {
			const c = doc.camera;
			if (!isVec3(c.pos)) err('/camera/pos', 'must be [x, y, z] metres');
			if (!isVec3(c.target)) err('/camera/target', 'must be [x, y, z] metres');
			if (c.fov !== undefined && !(isNum(c.fov) && c.fov > 0 && c.fov < 180))
				err('/camera/fov', 'must be a number of degrees between 0 and 180');
			if (c.projection !== undefined && c.projection !== 'perspective' && c.projection !== 'orthographic')
				err('/camera/projection', "must be 'perspective' or 'orthographic'");
			if (c.width !== undefined && !(isNum(c.width) && c.width > 0)) err('/camera/width', 'must be a number of metres > 0');
			if (c.width !== undefined && c.projection !== 'orthographic') err('/camera/width', 'only an orthographic camera has a width');
			if (c.fov !== undefined && c.projection === 'orthographic') err('/camera/fov', 'an orthographic camera has no field of view; use width');
		}
	}

	if (doc.grid !== undefined) {
		if (!isObj(doc.grid)) err('/grid', 'must be an object');
		else {
			if (doc.grid.pos !== undefined && !isVec3(doc.grid.pos)) err('/grid/pos', 'must be [x, y, z] metres');
			if (doc.grid.size !== undefined && !isVec2(doc.grid.size)) err('/grid/size', 'must be [w, d] metres');
			if (doc.grid.cell !== undefined && !(isNum(doc.grid.cell) && doc.grid.cell > 0)) err('/grid/cell', 'must be > 0');
			if (doc.grid.section !== undefined && !(isNum(doc.grid.section) && doc.grid.section > 0))
				err('/grid/section', 'must be > 0');
		}
	}

	if (doc.fixtures !== undefined) {
		if (!Array.isArray(doc.fixtures)) err('/fixtures', 'must be an array');
		else
			doc.fixtures.forEach((f, i) => {
				const p = `/fixtures/${i}`;
				if (!isObj(f)) return err(p, 'must be an object');
				if (f.kind !== 'box' && f.kind !== 'plane' && f.kind !== 'marker') err(`${p}/kind`, "must be 'box', 'plane' or 'marker'");
				if (!isVec3(f.pos)) err(`${p}/pos`, 'must be [x, y, z] metres');
				if (f.size !== undefined && !(isVec3(f.size) || isVec2(f.size))) err(`${p}/size`, 'must be [w, h, d] or [w, d]');
				if (f.rot !== undefined && !isVec3(f.rot)) err(`${p}/rot`, 'must be [x, y, z] degrees');
				if (f.opacity !== undefined && !(isNum(f.opacity) && f.opacity >= 0 && f.opacity <= 1))
					err(`${p}/opacity`, 'must be between 0 and 1');
				if (f.texture !== undefined) {
					if (f.kind !== 'plane') err(`${p}/texture`, 'only a plane takes a texture');
					if (!isObj(f.texture)) err(`${p}/texture`, 'must be { map, normalMap?, roughnessMap?, repeat? }');
					else {
						if (!isAssetPath(f.texture.map)) err(`${p}/texture/map`, 'must be a URL path to an image');
						for (const k of ['normalMap', 'roughnessMap'])
							if (f.texture[k] !== undefined && !isAssetPath(f.texture[k])) err(`${p}/texture/${k}`, 'must be a URL path to an image');
						if (f.texture.repeat !== undefined && !isVec2(f.texture.repeat)) err(`${p}/texture/repeat`, 'must be [w, d] tiles');
					}
				}
			});
	}

	if (doc.kinds !== undefined) {
		if (!isObj(doc.kinds)) err('/kinds', 'must be an object of kind -> { type, members }');
		else
			for (const [k, def] of Object.entries(doc.kinds)) {
				if (typeof def === 'string') {
					if (!isKindPath(def))
						err(`/kinds/${k}`, 'a kind file is a {Name}.kind.json as a path relative to the scene file (hmi/src/lib/kinds/pump.kind.json)');
					continue;
				}
				if (!isObj(def)) {
					err(`/kinds/${k}`, 'must be an object with type and/or members, or the path of a kind file');
					continue;
				}
				errors.push(...checkKindDef(def, `/kinds/${k}`, known, assemblies));
				// A kind the document defines is complete on its own; one it only
				// declares must be something the app registers.
				if (known && !known.has(k)) err(`/kinds/${k}`, `"${k}" is declared but the app registers no such kind`);
			}
	}

	if (doc.environment !== undefined) {
		if (!isObj(doc.environment)) err('/environment', 'must be an object');
		else {
			const e = doc.environment;
			if (e.hdri !== undefined && !(isAssetPath(e.hdri) && /\.(hdr|exr)$/i.test(e.hdri)))
				err('/environment/hdri', 'must be a URL path to an .hdr or .exr (env/workshop_1k.hdr)');
			if (e.backdrop !== undefined && !(isAssetPath(e.backdrop) && /\.(jpe?g|png|webp|hdr|exr)$/i.test(e.backdrop)))
				err('/environment/backdrop', 'must be a URL path to an equirect image (.jpg, .png, .webp, .hdr, .exr)');
			if (e.fog !== undefined) {
				if (!isObj(e.fog)) err('/environment/fog', 'must be { color?, near?, far? }');
				else {
					if (e.fog.color !== undefined && !isStr(e.fog.color)) err('/environment/fog/color', 'must be a CSS colour');
					if (e.fog.near !== undefined && !(isNum(e.fog.near) && e.fog.near >= 0)) err('/environment/fog/near', 'must be ≥ 0 metres');
					if (e.fog.far !== undefined && !(isNum(e.fog.far) && e.fog.far > (isNum(e.fog.near) ? e.fog.near : 0)))
						err('/environment/fog/far', 'must be greater than near');
				}
			}
			if (e.background !== undefined && e.background !== 'none' && e.background !== 'sky' && e.background !== 'ground')
				err('/environment/background', "must be 'none', 'sky' or 'ground'");
			if (e.floor !== undefined && !isNum(e.floor)) err('/environment/floor', 'must be a number of metres');
			if (e.intensity !== undefined && !(isNum(e.intensity) && e.intensity >= 0)) err('/environment/intensity', 'must be ≥ 0');
			if (e.shadows !== undefined && typeof e.shadows !== 'boolean') err('/environment/shadows', 'must be true or false');
		}
	}

	if (!Array.isArray(doc.nodes)) err('/nodes', 'must be an array');
	else {
		const ids = new Set<string>();
		doc.nodes.forEach((n, i) => checkNode(n, `/nodes/${i}`, err, known, ids));
	}

	if (doc.pipes !== undefined) {
		if (!Array.isArray(doc.pipes)) err('/pipes', 'must be an array');
		else {
			const frame: PipeFrame = { nodes: doc.nodes, portsOf: (k) => docPorts(doc, k) };
			doc.pipes.forEach((pipe, i) => checkPipe(pipe, `/pipes/${i}`, err, frame));
		}
	}

	if (doc.writable !== undefined) {
		if (!Array.isArray(doc.writable) || !doc.writable.every(isStr)) err('/writable', 'must be an array of tag names');
		else if (doc.writable.length > 0) err('/writable', 'writes from a scene are not supported yet; the list must be empty');
	}

	return { ok: errors.length === 0, errors };
}

/** The root tag a binding ref reads: `!P101.Fault` -> `P101`. */
export function refRoot(ref: string): string {
	const path = ref.startsWith('!') ? ref.slice(1) : ref;
	const dot = path.indexOf('.');
	return dot < 0 ? path : path.slice(0, dot);
}

/**
 * Every root tag the document reads, deduplicated, in document order: the
 * list a page hands `RealtimeClient({ tags })` so one filtered subscription
 * carries exactly what the scene shows.
 */
export function sceneTags(doc: SceneDoc): string[] {
	const out = new Set<string>();
	for (const n of doc.nodes) {
		if (n.tag) out.add(n.tag);
		for (const ref of Object.values(n.bind ?? {})) out.add(refRoot(ref));
	}
	for (const p of doc.pipes ?? []) if (p.bind?.flowing) out.add(refRoot(p.bind.flowing));
	return [...out];
}
