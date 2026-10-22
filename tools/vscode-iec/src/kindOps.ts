// The kind editor's reducer (docs/design/spatial-hmi.md §3g, slice 2): the
// sceneOps.ts of a `{Name}.kind.json`. A kind file holds what a scene's
// `kinds` entry holds — type, members, model, bounds, labelAt, status,
// drive — and every gesture in the kind editor is one op applied here to
// the text, as one WorkspaceEdit, so text undo covers a drive added from a
// mesh-name menu exactly as it covers a typed one. Pure: no vscode import,
// unit-tested with node --test (kindOps.test.ts).
//
// The rules are the structural half of hmi-3d's validateKind and
// internal/scene's checkKind: a drive names a mesh and exactly one channel
// of the six, an axis is x/y/z (xyz for scale), a bind names a member. What
// needs the model (a mesh that is not in it) is the webview's to say.

export type Vec3 = [number, number, number];

export type NumExpr = { bind: string; scale?: number; offset?: number; min?: number; max?: number };
export type BoolExpr = { bind: string; threshold?: number };
/** A colour over a number: stops of [value, colour], ascending. */
export type RampExpr = { bind: string; ramp: [number, string][] };
export type Drive = { mesh: string } & (
  | { spin: { axis: string; revPerS: NumExpr } }
  | { turn: { axis: string; deg: NumExpr } }
  | { scale: { axis: string; to: NumExpr } }
  | { tint: (BoolExpr & { on: string; off?: string }) | RampExpr }
  | { emissive: (BoolExpr & { on: string; intensity?: number }) | (RampExpr & { intensity?: number }) }
  | { visible: BoolExpr }
);

/** A place on a box (a part's, or the kind's bounds): survives a re-export. */
export type BoxPoint = { part?: string; anchor: string; offset?: Vec3 };
export type Point = Vec3 | BoxPoint;
/** A port (design §3g, slice 3): where a pipe meets the kind, in its frame. */
export type Port = { at: Point; dir?: Vec3 };

export type KindDoc = {
  type?: string;
  members?: string[];
  model?: string;
  bounds?: "auto" | { size: Vec3; center: Vec3 };
  /** Metres, an anchor word on the bounds, or a place on a part's box. */
  labelAt?: Point | string;
  status?: string;
  ports?: Record<string, Port>;
  drive?: Drive[];
};

/** The keys setField takes; `null` deletes the key. Mirror of
 * webview-ui/src/scene/kindState.svelte.ts. */
export type KindField = "type" | "members" | "model" | "bounds" | "labelAt" | "status";

export type KindOp =
  | { type: "batch"; ops: KindOp[] }
  | { type: "setField"; key: KindField; value: unknown }
  | { type: "addDrive"; drive: Drive; index?: number }
  | { type: "updateDrive"; index: number; drive: Drive }
  | { type: "deleteDrive"; index: number }
  | { type: "moveDrive"; index: number; to: number }
  /** Add or replace a port. */
  | { type: "setPort"; name: string; port: Port }
  | { type: "renamePort"; name: string; to: string }
  | { type: "deletePort"; name: string };

export const DRIVE_CHANNELS = ["spin", "turn", "scale", "tint", "emissive", "visible"] as const;
export type DriveChannel = (typeof DRIVE_CHANNELS)[number];

type Json = Record<string, unknown>;
const isObj = (v: unknown): v is Json => typeof v === "object" && v !== null && !Array.isArray(v);
const isNum = (v: unknown): v is number => typeof v === "number" && isFinite(v);
const isStr = (v: unknown): v is string => typeof v === "string" && v.length > 0;
const isVec3 = (v: unknown): v is Vec3 => Array.isArray(v) && v.length === 3 && v.every(isNum);
const MEMBER_RE = /^!?[A-Za-z_][A-Za-z0-9_]*$/;
const PORT_RE = /^[A-Za-z_][A-Za-z0-9_-]*$/;
export const ANCHOR_WORDS = ["center", "top", "bottom", "left", "right", "front", "back"] as const;
const AXIS_OF: Record<string, number> = { left: 0, right: 0, bottom: 1, top: 1, back: 2, front: 2 };
/** `top`, `top-front`, `center`: one or two face words, at most one per axis. */
export function isAnchorWord(v: unknown): v is string {
  if (!isStr(v)) return false;
  const w = v.split("-");
  if (w.length > 2 || !w.every((x) => (ANCHOR_WORDS as readonly string[]).includes(x))) return false;
  return w.length === 1 || (!w.includes("center") && AXIS_OF[w[0]] !== AXIS_OF[w[1]]);
}
export const isBoxPoint = (v: unknown): v is BoxPoint =>
  isObj(v) && isAnchorWord(v.anchor) && (v.part === undefined || isStr(v.part)) && (v.offset === undefined || isVec3(v.offset)) && Object.keys(v).every((k) => ["part", "anchor", "offset"].includes(k));
const POINT_HELP = "[x, y, z] metres, or { part?, anchor: top | bottom | left | right | front | back | center (or two, top-front), offset? }";
const PLAIN_MEMBER_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function parseKind(text: string): KindDoc {
  if (text.trim() === "") return {};
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw new Error(`not valid JSON: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (!isObj(raw)) throw new Error("a kind file is a JSON object: { model, status, drive, ... }");
  if (raw.component !== undefined || raw.assembly !== undefined)
    throw new Error("a kind file is a data kind (model + drives) or a contract — a component or an assembly kind stays inline in the scene");
  return raw as KindDoc;
}

// ── canonical serialization ────────────────────────────────────────────────

function ordered(obj: Json, keys: string[]): Json {
  const out: Json = {};
  for (const k of keys) if (obj[k] !== undefined) out[k] = obj[k];
  for (const k of Object.keys(obj)) if (!(k in out) && obj[k] !== undefined) out[k] = obj[k];
  return out;
}

const NUM = String.raw`-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?`;
const VEC_RE = new RegExp(String.raw`\[\n\s+(${NUM}),\n\s+(${NUM}),\n\s+(${NUM})\n\s+\]`, "g");

/** The channel a drive carries, if exactly one. */
export function driveChannel(d: unknown): DriveChannel | undefined {
  if (!isObj(d)) return undefined;
  const set = DRIVE_CHANNELS.filter((c) => d[c] !== undefined);
  return set.length === 1 ? set[0] : undefined;
}

/** A value on one line, the way a person writes a drive: `{ "mesh": "Fluid", "scale": { "axis": "y", "to": { "bind": "Level" } } }`. */
function compact(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(compact).join(", ")}]`;
  if (isObj(v)) {
    const parts = Object.entries(v).filter(([, x]) => x !== undefined).map(([k, x]) => `${JSON.stringify(k)}: ${compact(x)}`);
    return parts.length ? `{ ${parts.join(", ")} }` : "{}";
  }
  return JSON.stringify(v);
}

/** Serialize canonically: a fixed key order, 2-space indent, `[x, y, z]`
 * on one line, each drive `{ "mesh", <channel> }` on ONE line (a drive is
 * a row in the editor and a row in the diff), empty lists dropped. */
export function formatKind(doc: KindDoc): string {
  const d = ordered(doc as Json, ["type", "members", "model", "bounds", "labelAt", "status", "ports", "drive"]);
  if (Array.isArray(d.members) && d.members.length === 0) delete d.members;
  const drives = Array.isArray(d.drive) ? (d.drive as Json[]) : undefined;
  if (drives && drives.length === 0) delete d.drive;
  // Ports, one per line too: `"discharge": { "at": [0, 0.43, -0.5], "dir": [0, 1, 0] }`.
  const ports = isObj(d.ports) ? (d.ports as Record<string, Json>) : undefined;
  const portRows: string[] = [];
  if (ports && Object.keys(ports).length === 0) delete d.ports;
  else if (ports) {
    for (const [name, port] of Object.entries(ports)) {
      const o = ordered(port, ["at", "dir"]);
      if (isObj(o.at)) o.at = ordered(o.at as Json, ["part", "anchor", "offset"]);
      portRows.push(`${JSON.stringify(name)}: ${compact(o)}`);
    }
    d.ports = "\u0000ports";
  }
  if (d.bounds !== undefined && d.bounds !== "auto" && isObj(d.bounds)) d.bounds = ordered(d.bounds, ["size", "center"]);
  if (isObj(d.labelAt)) d.labelAt = ordered(d.labelAt, ["part", "anchor", "offset"]);
  const rows: string[] = [];
  if (drives && drives.length) {
    for (const drv of drives) {
      const ch = driveChannel(drv);
      const row = ordered(drv, ch ? ["mesh", ch] : ["mesh"]);
      if (ch && isObj(row[ch])) {
        const c = row[ch] as Json;
        const inner = ch === "spin" ? ["axis", "revPerS"] : ch === "turn" ? ["axis", "deg"] : ch === "scale" ? ["axis", "to"] : ["bind", "threshold", "on", "off", "ramp", "intensity"];
        const o = ordered(c, inner);
        for (const k of ["revPerS", "deg", "to"]) if (isObj(o[k])) o[k] = ordered(o[k] as Json, ["bind", "scale", "offset", "min", "max"]);
        row[ch] = o;
      }
      rows.push(compact(row));
    }
    // A placeholder keeps `drive` in its place; the rows go in after.
    d.drive = "\u0000drives";
  }
  let json = JSON.stringify(d, null, 2).replace(VEC_RE, (_m, a, b, c) => `[${a}, ${b}, ${c}]`);
  if (rows.length) json = json.replace('"\\u0000drives"', `[\n${rows.map((r) => "    " + r).join(",\n")}\n  ]`);
  if (portRows.length) json = json.replace('"\\u0000ports"', `{\n${portRows.map((r) => "    " + r).join(",\n")}\n  }`);
  return json + "\n";
}

/** Why a port is not one, or undefined when it is. */
export function portError(name: unknown, port: unknown): string | undefined {
  if (!isStr(name) || !PORT_RE.test(name)) return "a port name is letters, digits, _ or -";
  if (!isObj(port)) return "a port is { at: [x, y, z] | { part?, anchor, offset? }, dir?: [x, y, z] }";
  if (!(isVec3(port.at) || isBoxPoint(port.at))) return `at is ${POINT_HELP}`;
  if (port.dir !== undefined && !(isVec3(port.dir) && port.dir.some((c) => c !== 0))) return "dir is a non-zero [x, y, z] direction";
  return undefined;
}

// ── op application ─────────────────────────────────────────────────────────

/** Why a drive is not one, or undefined when it is. */
export function driveError(d: unknown): string | undefined {
  if (!isObj(d)) return "a drive is an object";
  if (!isStr(d.mesh)) return "a drive names a mesh in the model";
  const ch = driveChannel(d);
  if (!ch) return `a drive has exactly one of ${DRIVE_CHANNELS.join(", ")}`;
  const c = d[ch];
  if (!isObj(c)) return `${ch} must be an object`;
  const axis = (a: unknown, xyz: boolean) => (a === "x" || a === "y" || a === "z" || (xyz && a === "xyz") ? undefined : xyz ? "axis must be x, y, z or xyz" : "axis must be x, y or z");
  const num = (e: unknown, what: string) => {
    if (!isObj(e) || !isStr(e.bind) || !PLAIN_MEMBER_RE.test(e.bind)) return `${what}.bind must name a member of the struct`;
    for (const k of ["scale", "offset", "min", "max"]) if (e[k] !== undefined && !isNum(e[k])) return `${what}.${k} must be a number`;
    return undefined;
  };
  const bool = () => {
    if (!isStr(c.bind) || !MEMBER_RE.test(c.bind)) return "bind must name a member of the struct (a leading ! negates)";
    if (c.threshold !== undefined && !isNum(c.threshold)) return "threshold must be a number";
    return undefined;
  };
  const ramp = (): string | undefined => {
    if (!isStr(c.bind) || !PLAIN_MEMBER_RE.test(c.bind)) return "bind must name a member of the struct (a number)";
    const r = c.ramp;
    if (!Array.isArray(r) || r.length < 2) return "a ramp is at least two [value, colour] stops";
    let prev = -Infinity;
    for (const stop of r) {
      if (!Array.isArray(stop) || stop.length !== 2 || !isNum(stop[0]) || !isStr(stop[1])) return "a ramp stop is [value, colour]";
      if (!(stop[0] > prev)) return "ramp stops go up in value";
      prev = stop[0];
    }
    for (const k of ["on", "off", "threshold"]) if (c[k] !== undefined) return `${k} is not with a ramp`;
    return undefined;
  };
  switch (ch) {
    case "spin":
      return axis(c.axis, false) ?? num(c.revPerS, "revPerS");
    case "turn":
      return axis(c.axis, false) ?? num(c.deg, "deg");
    case "scale":
      return axis(c.axis, true) ?? num(c.to, "to");
    case "tint":
      if (c.ramp !== undefined) return ramp();
      return bool() ?? (isStr(c.on) ? (c.off !== undefined && !isStr(c.off) ? "off must be a palette slot or a CSS colour" : undefined) : "on must be a palette slot or a CSS colour");
    case "emissive":
      if (c.ramp !== undefined) return ramp() ?? (c.intensity !== undefined && !isNum(c.intensity) ? "intensity must be a number" : undefined);
      return bool() ?? (isStr(c.on) ? (c.intensity !== undefined && !isNum(c.intensity) ? "intensity must be a number" : undefined) : "on must be a palette slot or a CSS colour");
    case "visible":
      return bool();
  }
}

function fieldError(key: KindField, v: unknown): string | undefined {
  switch (key) {
    case "type":
      return isStr(v) ? undefined : "type is a UDT name";
    case "members":
      return Array.isArray(v) && v.every((m) => isStr(m) && PLAIN_MEMBER_RE.test(m)) ? undefined : "members is a list of member names";
    case "model":
      return isStr(v) && !/^[a-z]+:/i.test(v) && !v.split("/").includes("..") && /\.(glb|gltf)$/i.test(v) ? undefined : "model is a URL path to a .glb or .gltf (models/pump.glb)";
    case "bounds":
      return v === "auto" || (isObj(v) && isVec3(v.size) && isVec3(v.center)) ? undefined : "bounds is 'auto' or { size: [w, h, d], center: [x, y, z] }";
    case "labelAt":
      return isVec3(v) || isAnchorWord(v) || isBoxPoint(v) ? undefined : `labelAt is ${POINT_HELP}, or an anchor word on the bounds (top)`;
    case "status":
      return typeof v === "string" ? undefined : "status is a template over members";
  }
}

const mm = (v: number): number => Math.round(v * 1000) / 1000 + 0;
const roundVec = (v: Vec3): Vec3 => [mm(v[0]), mm(v[1]), mm(v[2])];
/** A box point with its offset rounded, in the canonical key order, without an empty part. */
const roundPoint = (p: BoxPoint): BoxPoint => {
  const out: BoxPoint = { anchor: p.anchor };
  if (p.part) out.part = p.part;
  if (p.offset && p.offset.some((c) => c !== 0)) out.offset = roundVec(p.offset);
  return out;
};

/** Apply one op to the document text; the result is the canonical text. */
export function applyKindOp(text: string, op: KindOp): { text: string } | { error: string } {
  let doc: KindDoc;
  try {
    doc = parseKind(text);
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
  const err = applyOpToDoc(doc, op);
  if (err) return { error: err };
  return { text: formatKind(doc) };
}

function applyOpToDoc(doc: KindDoc, op: KindOp): string | undefined {
  const drives = () => (doc.drive ??= []);
  switch (op.type) {
    case "batch": {
      for (const o of op.ops) {
        const e = applyOpToDoc(doc, o);
        if (e) return e;
      }
      return undefined;
    }
    case "setField": {
      const d = doc as Json;
      if (!["type", "members", "model", "bounds", "labelAt", "status"].includes(op.key)) return `unknown field ${String(op.key)}`;
      if (op.value === null || op.value === undefined) {
        delete d[op.key];
        return undefined;
      }
      const e = fieldError(op.key, op.value);
      if (e) return e;
      let v = op.value;
      if (op.key === "labelAt" && isVec3(v)) v = roundVec(v);
      else if (op.key === "labelAt" && isBoxPoint(v)) v = roundPoint(v);
      if (op.key === "bounds" && isObj(v)) v = { size: roundVec(v.size as Vec3), center: roundVec(v.center as Vec3) };
      d[op.key] = v;
      return undefined;
    }
    case "addDrive": {
      const e = driveError(op.drive);
      if (e) return e;
      const list = drives();
      const at = op.index === undefined ? list.length : op.index;
      if (!Number.isInteger(at) || at < 0 || at > list.length) return `drive index ${op.index} out of range`;
      list.splice(at, 0, op.drive);
      return undefined;
    }
    case "updateDrive": {
      const list = drives();
      if (!Number.isInteger(op.index) || op.index < 0 || op.index >= list.length) return `no drive ${op.index}`;
      const e = driveError(op.drive);
      if (e) return e;
      list[op.index] = op.drive;
      return undefined;
    }
    case "deleteDrive": {
      const list = drives();
      if (!Number.isInteger(op.index) || op.index < 0 || op.index >= list.length) return `no drive ${op.index}`;
      list.splice(op.index, 1);
      return undefined;
    }
    case "moveDrive": {
      const list = drives();
      if (!Number.isInteger(op.index) || op.index < 0 || op.index >= list.length) return `no drive ${op.index}`;
      if (!Number.isInteger(op.to) || op.to < 0 || op.to >= list.length) return `no drive slot ${op.to}`;
      const [d] = list.splice(op.index, 1);
      list.splice(op.to, 0, d);
      return undefined;
    }
    case "setPort": {
      const e = portError(op.name, op.port);
      if (e) return e;
      const port: Port = { at: isVec3(op.port.at) ? roundVec(op.port.at) : roundPoint(op.port.at) };
      if (op.port.dir) port.dir = roundVec(op.port.dir);
      (doc.ports ??= {})[op.name] = port;
      return undefined;
    }
    case "renamePort": {
      const ports = doc.ports ?? {};
      if (!(op.name in ports)) return `no port "${op.name}"`;
      if (!isStr(op.to) || !PORT_RE.test(op.to)) return "a port name is letters, digits, _ or -";
      if (op.to === op.name) return undefined;
      if (op.to in ports) return `a port "${op.to}" already exists`;
      // Keep the port's place in the list: rebuild in order.
      const next: Record<string, Port> = {};
      for (const [k, v] of Object.entries(ports)) next[k === op.name ? op.to : k] = v;
      doc.ports = next;
      return undefined;
    }
    case "deletePort": {
      const ports = doc.ports ?? {};
      if (!(op.name in ports)) return `no port "${op.name}"`;
      delete ports[op.name];
      if (Object.keys(ports).length === 0) delete doc.ports;
      return undefined;
    }
    default:
      return `unknown op ${(op as { type: string }).type}`;
  }
}
