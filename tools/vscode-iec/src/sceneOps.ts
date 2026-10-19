// Scene op reducer: every gesture in the 3D scene editor on a *.scene.json
// document becomes a STRUCTURAL OP applied here, in the extension host —
// pure TypeScript, the mimic reducer's (mimicOps.ts) spatial twin. An op
// resolves against a fresh parse of the current buffer and comes back as
// the full re-serialized document; the provider applies it as one
// WorkspaceEdit so VS Code's text-document undo covers every drag.
//
// Serialization is CANONICAL: fixed key order, 2-space indent, a [x, y, z]
// triple on one line, empty collections omitted. A hand-written (or
// AI-written) document is normalized on its first edit; after that, a drag
// diffs as one `pos` line — which is the point: the AI writes the scene,
// a person nudges it, and the review shows exactly the nudge.

/** Mirror of hmi-3d/src/lib/scene.ts — the *.scene.json contract. Only
 * the keys the reducer touches are typed; everything else (kinds,
 * environment, textures, props) passes through untouched. */
export type Vec3 = [number, number, number];

export type SceneNode = {
  id: string;
  kind: string;
  tag?: string;
  label?: string;
  pos: Vec3;
  rot?: Vec3;
  scale?: number;
  props?: Record<string, unknown>;
  bind?: Record<string, string>;
};

export type ScenePipe = {
  id?: string;
  points: Vec3[];
  radius?: number;
  bind?: { flowing?: string };
};

export type SceneCamera = { pos: Vec3; target: Vec3; fov?: number };

export type SceneDoc = {
  name?: string;
  look?: string;
  kinds?: Record<string, unknown>;
  environment?: Record<string, unknown>;
  camera?: SceneCamera;
  grid?: Record<string, unknown>;
  fixtures?: Record<string, unknown>[];
  nodes: SceneNode[];
  pipes?: ScenePipe[];
};

/** Patch semantics: a key set to null DELETES the optional field; a present
 * value sets it. Mirror of webview-ui/src/scene/sceneState.svelte.ts. */
export type SceneOp =
  | { type: "batch"; ops: SceneOp[] }
  | { type: "addNode"; kind: string; pos: Vec3; id?: string; tag?: string; label?: string }
  | { type: "moveNode"; id: string; pos: Vec3; rot?: Vec3 }
  | { type: "updateNode"; id: string; patch: Record<string, unknown> }
  | { type: "deleteNode"; id: string }
  | { type: "addPipe"; points: Vec3[]; id?: string; bind?: { flowing?: string } }
  | { type: "setPipePoints"; index: number; points: Vec3[] }
  | { type: "deletePipe"; index: number }
  | { type: "setCamera"; pos: Vec3; target: Vec3 };

export const EMPTY_DOC: SceneDoc = { nodes: [] };

/** Parse buffer text into a SceneDoc. Empty/whitespace = the starter doc.
 * Throws with a human message on bad JSON or a non-object root. */
export function parseScene(text: string): SceneDoc {
  if (text.trim() === "") return structuredClone(EMPTY_DOC);
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw new Error(`not valid JSON: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new Error("root must be a JSON object");
  }
  const doc = raw as SceneDoc;
  if (doc.nodes === undefined) doc.nodes = [];
  if (!Array.isArray(doc.nodes)) throw new Error('"nodes" must be a list');
  return doc;
}

// ── canonical serialization ────────────────────────────────────────────────

function ordered(obj: Record<string, unknown>, keys: string[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of keys) if (obj[k] !== undefined) out[k] = obj[k];
  for (const k of Object.keys(obj)) if (!(k in out) && obj[k] !== undefined) out[k] = obj[k];
  return out;
}

const emptyObj = (v: unknown): boolean =>
  typeof v === "object" && v !== null && !Array.isArray(v) && Object.keys(v).length === 0;

const NUM = String.raw`-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?`;
/** A two- or three-number array, as JSON.stringify indents it. */
const VEC_RE = new RegExp(String.raw`\[\n\s+(${NUM}),\n\s+(${NUM})(?:,\n\s+(${NUM}))?\n\s+\]`, "g");

/** Serialize canonically: stable key order, 2-space indent, `[x, y, z]`
 * triples (and `[w, d]` pairs) on one line, empty collections dropped. */
export function formatScene(doc: SceneDoc): string {
  const d = ordered(doc as Record<string, unknown>, [
    "name",
    "look",
    "kinds",
    "environment",
    "camera",
    "grid",
    "fixtures",
    "nodes",
    "pipes",
  ]);
  for (const key of ["fixtures", "nodes", "pipes"]) {
    const arr = d[key] as Record<string, unknown>[] | undefined;
    if (!arr || (arr.length === 0 && key !== "nodes")) {
      if (key !== "nodes") delete d[key];
      else d[key] = [];
      continue;
    }
    const order =
      key === "nodes"
        ? ["id", "kind", "tag", "label", "pos", "rot", "scale", "props", "bind"]
        : key === "pipes"
          ? ["id", "points", "radius", "bind"]
          : ["kind", "pos", "size", "rot", "color", "opacity", "texture"];
    d[key] = arr.map((item) => {
      const o = ordered(item, order);
      for (const k of ["props", "bind"]) if (emptyObj(o[k])) delete o[k];
      return o;
    });
  }
  if (emptyObj(d.kinds)) delete d.kinds;
  const json = JSON.stringify(d, null, 2).replace(VEC_RE, (_m, a, b, c) => (c === undefined ? `[${a}, ${b}]` : `[${a}, ${b}, ${c}]`));
  return json + "\n";
}

// ── op application ─────────────────────────────────────────────────────────

/** A document id: what a pick reports, what a pipe will anchor to. */
const ID_RE = /^[A-Za-z_][A-Za-z0-9_-]*$/;

/** First free id of the form `<stem><n>` among nodes and pipes. The stem
 * is the kind, upper-cased the way a tag is (`pump` → `PUMP1`), so a
 * placed node's id already looks like the tag it will bind. */
function genId(doc: SceneDoc, stem: string): string {
  const base = stem.replace(/[^A-Za-z0-9_]/g, "").toUpperCase() || "NODE";
  for (let n = 1; ; n++) {
    const id = `${base}${n}`;
    if (!idTaken(doc, id)) return id;
  }
}

const idTaken = (doc: SceneDoc, id: string): boolean =>
  doc.nodes.some((n) => n.id === id) || (doc.pipes ?? []).some((p) => p.id === id);

function applyPatch(target: Record<string, unknown>, patch: Record<string, unknown>): void {
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete target[k];
    else target[k] = v;
  }
}

const isNum = (v: unknown): v is number => typeof v === "number" && isFinite(v);
const isVec3 = (v: unknown): v is Vec3 => Array.isArray(v) && v.length === 3 && v.every(isNum);

/** Millimetre positions, tenth-degree rotations: what a drag produces is
 * a float with twelve decimals, which no one wants to read in a diff. */
const mm = (v: number): number => Math.round(v * 1000) / 1000 + 0;
const tenth = (v: number): number => Math.round(v * 10) / 10 + 0;
export const roundPos = (p: Vec3): Vec3 => [mm(p[0]), mm(p[1]), mm(p[2])];
export const roundRot = (r: Vec3): Vec3 => [tenth(r[0]), tenth(r[1]), tenth(r[2])];
const isZero = (r: Vec3): boolean => r[0] === 0 && r[1] === 0 && r[2] === 0;

/** Apply one op to the document text. Returns the full new text, or a
 * refusal message (unknown id, invalid patch, bad JSON) — refusals never
 * write. A "batch" op is atomic: all sub-ops apply, or none do. */
export function applySceneOp(text: string, op: SceneOp): { text: string } | { error: string } {
  let doc: SceneDoc;
  try {
    doc = parseScene(text);
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
  const error = applyOpToDoc(doc, op);
  if (error !== undefined) return { error };
  return { text: formatScene(doc) };
}

function applyOpToDoc(doc: SceneDoc, op: SceneOp): string | undefined {
  switch (op.type) {
    case "batch": {
      for (const sub of op.ops) {
        const err = applyOpToDoc(doc, sub);
        if (err !== undefined) return err;
      }
      break;
    }
    case "addNode": {
      if (typeof op.kind !== "string" || op.kind === "") return "a node needs a kind";
      if (!isVec3(op.pos)) return "pos must be [x, y, z] metres";
      const id = op.id ?? genId(doc, op.kind);
      if (!ID_RE.test(id)) return `id "${id}" must be a name (letters, digits, _ or -)`;
      if (idTaken(doc, id)) return `id "${id}" is already taken`;
      const node: SceneNode = { id, kind: op.kind, pos: roundPos(op.pos) };
      if (op.tag) node.tag = op.tag;
      if (op.label) node.label = op.label;
      doc.nodes.push(node);
      break;
    }
    case "moveNode": {
      const node = doc.nodes.find((n) => n.id === op.id);
      if (!node) return `no node "${op.id}"`;
      if (!isVec3(op.pos)) return "pos must be [x, y, z] metres";
      node.pos = roundPos(op.pos);
      if (op.rot !== undefined) {
        if (!isVec3(op.rot)) return "rot must be [x, y, z] degrees";
        const r = roundRot(op.rot);
        if (isZero(r)) delete node.rot;
        else node.rot = r;
      }
      break;
    }
    case "updateNode": {
      const node = doc.nodes.find((n) => n.id === op.id);
      if (!node) return `no node "${op.id}"`;
      const rename = op.patch.id;
      if (rename !== undefined) {
        if (typeof rename !== "string" || !ID_RE.test(rename)) return "id must be a name (letters, digits, _ or -)";
        if (rename !== node.id && idTaken(doc, rename)) return `id "${rename}" is already taken`;
      }
      if (op.patch.kind !== undefined && (typeof op.patch.kind !== "string" || op.patch.kind === "")) return "kind must be a name";
      if (op.patch.pos !== undefined && !isVec3(op.patch.pos)) return "pos must be [x, y, z] metres";
      if (op.patch.rot !== undefined && op.patch.rot !== null && !isVec3(op.patch.rot)) return "rot must be [x, y, z] degrees";
      if (op.patch.scale !== undefined && op.patch.scale !== null && !(isNum(op.patch.scale) && op.patch.scale > 0)) return "scale must be a positive number";
      for (const k of ["tag", "label"]) {
        const v = op.patch[k];
        if (v !== undefined && v !== null && typeof v !== "string") return `${k} must be text`;
        if (v === "") op.patch[k] = null;
      }
      if (op.patch.bind !== undefined && op.patch.bind !== null) {
        const b = op.patch.bind;
        if (typeof b !== "object" || Array.isArray(b) || !Object.values(b as object).every((v) => typeof v === "string")) {
          return "bind must map prop names to refs";
        }
      }
      applyPatch(node as unknown as Record<string, unknown>, op.patch);
      if (isVec3(node.pos)) node.pos = roundPos(node.pos);
      if (node.rot && isZero(roundRot(node.rot))) delete node.rot;
      else if (node.rot) node.rot = roundRot(node.rot);
      if (node.scale === 1) delete node.scale;
      break;
    }
    case "deleteNode": {
      if (!doc.nodes.some((n) => n.id === op.id)) return `no node "${op.id}"`;
      doc.nodes = doc.nodes.filter((n) => n.id !== op.id);
      break;
    }
    case "addPipe": {
      if (!validPoints(op.points)) return "a pipe needs at least two [x, y, z] points";
      const pipe: ScenePipe = { points: op.points.map(roundPos) };
      if (op.id !== undefined) {
        if (!ID_RE.test(op.id)) return `id "${op.id}" must be a name`;
        if (idTaken(doc, op.id)) return `id "${op.id}" is already taken`;
        pipe.id = op.id;
      }
      if (op.bind?.flowing) pipe.bind = { flowing: op.bind.flowing };
      (doc.pipes ??= []).push(pipe);
      break;
    }
    case "setPipePoints": {
      const pipe = (doc.pipes ?? [])[op.index];
      if (!pipe) return `no pipe #${op.index}`;
      if (!validPoints(op.points)) return "a pipe needs at least two [x, y, z] points";
      pipe.points = op.points.map(roundPos);
      break;
    }
    case "deletePipe": {
      const list = doc.pipes ?? [];
      if (!list[op.index]) return `no pipe #${op.index}`;
      doc.pipes = list.filter((_, i) => i !== op.index);
      break;
    }
    case "setCamera": {
      if (!isVec3(op.pos) || !isVec3(op.target)) return "a camera needs pos and target as [x, y, z]";
      doc.camera = { ...(doc.camera ?? {}), pos: roundPos(op.pos), target: roundPos(op.target) };
      break;
    }
    default:
      return `unknown op "${(op as { type?: string }).type}"`;
  }
  return undefined;
}

function validPoints(points: unknown): points is Vec3[] {
  return Array.isArray(points) && points.length >= 2 && points.every(isVec3);
}
