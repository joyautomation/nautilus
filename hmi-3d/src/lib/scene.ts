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

export type Vec3 = [number, number, number];

export interface SceneCamera {
	pos: Vec3;
	target: Vec3;
	/** Vertical field of view, degrees. Default 45. */
	fov?: number;
}

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

export interface ScenePipe {
	id?: string;
	/** Polyline in scene metres, at least two points. */
	points: Vec3[];
	/** Metres. Default 0.012. */
	radius?: number;
	/** `flowing` is the one live prop a pipe has, the same key the mimic's
	 * pipe binds. Booleans as-is; a number is flowing above 2 (%). */
	bind?: { flowing?: string };
}

export interface SceneDoc {
	name?: string;
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

/** Is `ref` a well-formed binding ref? `!` may lead; then a dotted path of
 * non-empty segments. (The grammar is the mimic's plus dots; bindings.ts.) */
export function isBindingRef(ref: unknown): boolean {
	if (!isStr(ref)) return false;
	const path = ref.startsWith('!') ? ref.slice(1) : ref;
	return path.length > 0 && path.split('.').every((s) => s.length > 0);
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

	if (!isObj(doc)) return { ok: false, errors: [{ path: '', message: 'scene must be an object' }] };

	if (doc.camera !== undefined) {
		if (!isObj(doc.camera)) err('/camera', 'must be an object');
		else {
			if (!isVec3(doc.camera.pos)) err('/camera/pos', 'must be [x, y, z] metres');
			if (!isVec3(doc.camera.target)) err('/camera/target', 'must be [x, y, z] metres');
			if (doc.camera.fov !== undefined && !(isNum(doc.camera.fov) && doc.camera.fov > 0 && doc.camera.fov < 180))
				err('/camera/fov', 'must be a number of degrees between 0 and 180');
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
			});
	}

	if (!Array.isArray(doc.nodes)) err('/nodes', 'must be an array');
	else {
		const ids = new Set<string>();
		doc.nodes.forEach((n, i) => {
			const p = `/nodes/${i}`;
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
		});
	}

	if (doc.pipes !== undefined) {
		if (!Array.isArray(doc.pipes)) err('/pipes', 'must be an array');
		else
			doc.pipes.forEach((pipe, i) => {
				const p = `/pipes/${i}`;
				if (!isObj(pipe)) return err(p, 'must be an object');
				if (!Array.isArray(pipe.points) || pipe.points.length < 2) err(`${p}/points`, 'needs at least two points');
				else pipe.points.forEach((pt, j) => isVec3(pt) || err(`${p}/points/${j}`, 'must be [x, y, z] metres'));
				if (pipe.radius !== undefined && !(isNum(pipe.radius) && pipe.radius > 0)) err(`${p}/radius`, 'must be > 0');
				if (pipe.bind !== undefined) {
					if (!isObj(pipe.bind)) err(`${p}/bind`, 'must be an object');
					else {
						for (const k of Object.keys(pipe.bind)) if (k !== 'flowing') err(`${p}/bind/${k}`, "a pipe binds only 'flowing'");
						if (pipe.bind.flowing !== undefined && !isBindingRef(pipe.bind.flowing))
							err(`${p}/bind/flowing`, `"${String(pipe.bind.flowing)}" is not a tag ref`);
					}
				}
			});
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
