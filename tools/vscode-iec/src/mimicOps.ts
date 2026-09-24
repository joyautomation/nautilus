// Mimic op reducer: every editor gesture on a *.mimic.json document becomes
// a STRUCTURAL OP applied here, in the extension host — pure TypeScript, no
// CLI round-trip (JSON needs no Go parser; same "text is canonical"
// philosophy as .fbd, minus the subprocess). An op resolves against a fresh
// parse of the current buffer and comes back as the full re-serialized
// document; the provider applies it as one WorkspaceEdit so VS Code's
// text-document undo covers every gesture.
//
// Serialization is CANONICAL: fixed key order, 2-space indent, point pairs
// on one line, empty collections omitted. Hand-written docs get normalized
// on their first edit; after that, diffs are minimal and reviewable.

/** Mirror of hmi/src/lib/mimic.ts — the *.mimic.json contract. */
export type MimicDoc = {
  name?: string;
  canvas: { width: number; height: number };
  equipment?: MimicEquipment[];
  pipes?: MimicPipe[];
  labels?: MimicLabel[];
};
/** The direction a pipe LEAVES a port. Mirror of hmi/src/lib/mimic.ts's
 * PortDir. */
export type PortDir = "left" | "right" | "up" | "down";

/** A named connection point, as a fraction of the rendered box. Mirror of
 * webview-ui/src/mimic/portsGestures.ts's Port + hmi/src/lib/mimic.ts's
 * MimicPort. The name is how a pipe end anchors to it explicitly
 * (MimicPipe.from/to below). `dir` is the direction a pipe leaves the port;
 * absent = inferred from its edge position (hmi's inferPortDir — mirrored
 * nowhere else since this reducer never needs to COMPUTE the inference,
 * only validate the explicit override's shape). Names are unique within a
 * port list. */
export type Port = { name: string; x: number; y: number; dir?: PortDir };

/** A pipe end's attachment to a named equipment port. Mirror of
 * hmi/src/lib/mimic.ts's MimicPipeAnchor. */
export type PipeAnchor = { equip: string; port: string };

export type MimicEquipment = {
  id: string;
  component: string;
  x: number;
  y: number;
  width?: number;
  label?: string;
  props?: Record<string, unknown>;
  bind?: Record<string, string>;
  /** Connection-point override, named fractions of the rendered box.
   * Editor-only (see the {ComponentName}.component.json sidecar convention,
   * mimicComponents.ts, for the component-level tier); absent means "inherit",
   * explicit `[]` means "no ports". */
  ports?: Port[];
};
export type MimicPipe = {
  id: string;
  /** Interior vertices only when an end is anchored (see `from`/`to`) —
   * otherwise the full point list, same as always. */
  points: [number, number][];
  color?: string;
  /** How consecutive points are connected — see hmi/src/lib/mimic.ts's
   * MimicPipe (the canonical doc-level definition this mirrors): 'direct'
   * (default, absent) draws a straight segment between each pair;
   * 'orthogonal' routes each pair through a single 90° corner. A per-pipe
   * doc property (not an editor preference) because both the mimic editor
   * and the runtime <Mimic> render pipes and must draw identically. */
  routing?: "direct" | "orthogonal";
  /** Anchor the pipe's first/last point to a named equipment port instead
   * of a stored [x, y] — see PipeAnchor / hmi/src/lib/mimic.ts's
   * MimicPipeAnchor. `equip`/`port` need not resolve to anything that
   * exists — an unresolvable anchor still renders (mimic.ts's
   * resolvePipeEndpoints falls back rather than refusing), so this reducer
   * only validates SHAPE, never existence. */
  from?: PipeAnchor;
  to?: PipeAnchor;
  props?: Record<string, unknown>;
  bind?: Record<string, string>;
};
export type MimicLabel = { text: string; x: number; y: number };

/** Patch semantics: a key present with `null` DELETES the optional field;
 * a key present with a value sets it; an absent key is left alone. (JSON
 * message passing drops `undefined`, so `null` is the deletion signal.) */
export type MimicOp =
  | { type: "batch"; ops: MimicOp[] }
  | { type: "setName"; name?: string | null }
  | { type: "setCanvas"; width: number; height: number }
  | { type: "addEquipment"; component: string; x: number; y: number; id?: string }
  | { type: "moveEquipment"; id: string; x: number; y: number }
  | { type: "updateEquipment"; id: string; patch: Record<string, unknown> }
  | { type: "deleteEquipment"; id: string }
  | { type: "setEquipmentPorts"; id: string; ports: Port[] | null }
  | {
      type: "addPipe";
      points: [number, number][];
      id?: string;
      from?: PipeAnchor;
      to?: PipeAnchor;
      /** Route-suggestion gesture (autoroute.ts): a pipe created port-to-port
       * with a suggested route defaults to 'orthogonal' — see the webview's
       * addPipe call site. Absent = 'direct', same as always. */
      routing?: "direct" | "orthogonal";
    }
  | { type: "setPipePoints"; id: string; points: [number, number][] }
  | { type: "updatePipe"; id: string; patch: Record<string, unknown> }
  | { type: "deletePipe"; id: string }
  | { type: "addLabel"; text: string; x: number; y: number }
  | { type: "updateLabel"; index: number; patch: Record<string, unknown> }
  | { type: "deleteLabel"; index: number };

/** A brand-new (empty) file renders as this starter canvas; the first op
 * writes the full document. */
export const EMPTY_DOC: MimicDoc = { canvas: { width: 960, height: 540 } };

/** Parse buffer text into a MimicDoc. Empty/whitespace = the starter doc.
 * Throws with a human message on bad JSON or a non-object root. */
export function parseMimic(text: string): MimicDoc {
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
  const doc = raw as MimicDoc;
  if (
    typeof doc.canvas !== "object" ||
    doc.canvas === null ||
    typeof doc.canvas.width !== "number" ||
    typeof doc.canvas.height !== "number"
  ) {
    throw new Error('missing "canvas": {"width": …, "height": …}');
  }
  return doc;
}

// ── canonical serialization ────────────────────────────────────────────────

/** Rebuild an object with a fixed key order (known keys first, unknown
 * extras after, in their original order) and without undefined values. */
function ordered(obj: Record<string, unknown>, keys: string[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of keys) if (obj[k] !== undefined) out[k] = obj[k];
  for (const k of Object.keys(obj)) if (!(k in out) && obj[k] !== undefined) out[k] = obj[k];
  return out;
}

const emptyObj = (v: unknown): boolean =>
  typeof v === "object" && v !== null && !Array.isArray(v) && Object.keys(v).length === 0;

/** Serialize canonically: stable key order, 2-space indent, `[x, y]` point
 * pairs kept on one line, empty collections dropped. */
export function formatMimic(doc: MimicDoc): string {
  const d: Record<string, unknown> = ordered(doc as Record<string, unknown>, [
    "name",
    "canvas",
    "equipment",
    "pipes",
    "labels",
  ]);
  for (const key of ["equipment", "pipes", "labels"]) {
    const arr = d[key] as Record<string, unknown>[] | undefined;
    if (!arr || arr.length === 0) {
      delete d[key];
      continue;
    }
    const order =
      key === "equipment"
        ? ["id", "component", "x", "y", "width", "label", "props", "bind", "ports"]
        : key === "pipes"
          ? ["id", "points", "routing", "from", "to", "color", "props", "bind"]
          : ["text", "x", "y"];
    d[key] = arr.map((item) => {
      const o = ordered(item, order);
      for (const k of ["props", "bind"]) if (emptyObj(o[k])) delete o[k];
      return o;
    });
  }
  // Collapse two-number arrays (pipe points) back onto one line.
  const json = JSON.stringify(d, null, 2).replace(
    /\[\n\s+(-?\d+(?:\.\d+)?),\n\s+(-?\d+(?:\.\d+)?)\n\s+\]/g,
    "[$1, $2]"
  );
  return json + "\n";
}

// ── op application ─────────────────────────────────────────────────────────

/** First free id of the form `<stem><n>` among equipment and pipes. */
function genId(doc: MimicDoc, stem: string): string {
  const taken = new Set<string>([
    ...(doc.equipment ?? []).map((e) => e.id),
    ...(doc.pipes ?? []).map((p) => p.id),
  ]);
  for (let n = 1; ; n++) {
    const id = `${stem}${n}`;
    if (!taken.has(id)) return id;
  }
}

const idTaken = (doc: MimicDoc, id: string): boolean =>
  (doc.equipment ?? []).some((e) => e.id === id) || (doc.pipes ?? []).some((p) => p.id === id);

/** Apply a patch in place: null deletes, value sets (see MimicOp docs). */
function applyPatch(target: Record<string, unknown>, patch: Record<string, unknown>): void {
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete target[k];
    else target[k] = v;
  }
}

/** `points` must have at least `minLen` [x, y] pairs — a plain (no anchors)
 * pipe still needs 2 (the historical rule); an anchored end supplies its
 * own endpoint, so each anchored end lowers the floor by one (see
 * minInteriorPoints below). */
function validPointsList(points: unknown, minLen: number): points is [number, number][] {
  return (
    Array.isArray(points) &&
    points.length >= minLen &&
    points.every(
      (p) => Array.isArray(p) && p.length === 2 && p.every((n) => typeof n === "number" && isFinite(n))
    )
  );
}

/** How many INTERIOR [x, y] points a pipe needs given which ends are
 * anchored — a plain pipe still needs 2 points (start + end); each anchored
 * end supplies its own endpoint instead, so the floor drops by one per
 * anchored end (both anchored -> 0, an empty interior is a valid pipe: just
 * the two ports, straight or dir-stubbed between them). */
function minInteriorPoints(fromSet: boolean, toSet: boolean): number {
  return Math.max(0, 2 - (fromSet ? 1 : 0) - (toSet ? 1 : 0));
}

const DIRS = new Set(["left", "right", "up", "down"]);

function validAnchor(a: unknown): a is PipeAnchor {
  return (
    typeof a === "object" &&
    a !== null &&
    typeof (a as PipeAnchor).equip === "string" &&
    (a as PipeAnchor).equip !== "" &&
    typeof (a as PipeAnchor).port === "string" &&
    (a as PipeAnchor).port !== ""
  );
}

/** A ports list may be empty (an explicit "no ports" override); each entry
 * must be a named fraction pair with an optional dir in the 4-value enum,
 * and names must be unique within the list — see mimicComponentIndex.ts's
 * validatePortList, the sidecar-side twin of this check. */
function validPortList(ports: unknown): ports is Port[] {
  if (!Array.isArray(ports)) return false;
  if (
    !ports.every(
      (p): p is Port =>
        typeof p === "object" &&
        p !== null &&
        typeof (p as Port).name === "string" &&
        (p as Port).name !== "" &&
        typeof (p as Port).x === "number" &&
        isFinite((p as Port).x) &&
        typeof (p as Port).y === "number" &&
        isFinite((p as Port).y) &&
        ((p as Port).dir === undefined || DIRS.has((p as Port).dir as string))
    )
  ) {
    return false;
  }
  const names = (ports as Port[]).map((p) => p.name);
  return new Set(names).size === names.length;
}

/** Apply one op to the document text. Returns the full new text, or a
 * refusal message (unknown id, invalid patch, bad JSON) — refusals never
 * write. A "batch" op is atomic: all sub-ops apply, or none do (the editor
 * uses it for equipment moves that carry attached pipe ends along, so the
 * whole gesture is one undo step). */
export function applyMimicOp(text: string, op: MimicOp): { text: string } | { error: string } {
  let doc: MimicDoc;
  try {
    doc = parseMimic(text);
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
  const error = applyOpToDoc(doc, op);
  if (error !== undefined) return { error };
  return { text: formatMimic(doc) };
}

/** Mutate the doc per one op; returns a refusal message or undefined. */
function applyOpToDoc(doc: MimicDoc, op: MimicOp): string | undefined {
  switch (op.type) {
    case "batch": {
      for (const sub of op.ops) {
        const err = applyOpToDoc(doc, sub);
        if (err !== undefined) return err;
      }
      break;
    }
    case "setName": {
      if (op.name === null || op.name === undefined || op.name === "") delete doc.name;
      else doc.name = op.name;
      break;
    }
    case "setCanvas": {
      if (!(op.width > 0) || !(op.height > 0)) return "canvas size must be positive";
      doc.canvas = { width: op.width, height: op.height };
      break;
    }
    case "addEquipment": {
      const id = op.id ?? genId(doc, op.component.toLowerCase());
      if (idTaken(doc, id)) return `id "${id}" is already taken`;
      (doc.equipment ??= []).push({ id, component: op.component, x: op.x, y: op.y });
      break;
    }
    case "moveEquipment": {
      const eq = (doc.equipment ?? []).find((e) => e.id === op.id);
      if (!eq) return `no equipment "${op.id}"`;
      eq.x = op.x;
      eq.y = op.y;
      break;
    }
    case "updateEquipment": {
      const eq = (doc.equipment ?? []).find((e) => e.id === op.id);
      if (!eq) return `no equipment "${op.id}"`;
      const rename = op.patch.id;
      if (rename !== undefined) {
        if (typeof rename !== "string" || rename === "") return "id must be a non-empty string";
        if (rename !== eq.id && idTaken(doc, rename)) return `id "${rename}" is already taken`;
      }
      const oldId = eq.id;
      applyPatch(eq as unknown as Record<string, unknown>, op.patch);
      // A rename carries every pipe anchored to the old id along with it, in
      // the same edit — otherwise the pipes silently detach (an anchor to a
      // missing equip falls back to its stored point).
      if (typeof rename === "string" && rename !== oldId) {
        for (const p of doc.pipes ?? []) {
          if (p.from?.equip === oldId) p.from = { ...p.from, equip: rename };
          if (p.to?.equip === oldId) p.to = { ...p.to, equip: rename };
        }
      }
      break;
    }
    case "deleteEquipment": {
      const list = doc.equipment ?? [];
      if (!list.some((e) => e.id === op.id)) return `no equipment "${op.id}"`;
      doc.equipment = list.filter((e) => e.id !== op.id);
      break;
    }
    case "setEquipmentPorts": {
      const eq = (doc.equipment ?? []).find((e) => e.id === op.id);
      if (!eq) return `no equipment "${op.id}"`;
      if (op.ports === null) delete eq.ports;
      else {
        if (!validPortList(op.ports)) return "ports must be a list of uniquely-named {name, x, y} fraction points";
        eq.ports = op.ports;
      }
      break;
    }
    case "addPipe": {
      const fromSet = op.from !== undefined;
      const toSet = op.to !== undefined;
      if (fromSet && !validAnchor(op.from)) return "from must be {equip, port}";
      if (toSet && !validAnchor(op.to)) return "to must be {equip, port}";
      const minLen = minInteriorPoints(fromSet, toSet);
      if (!validPointsList(op.points, minLen)) {
        return minLen > 0
          ? `a pipe needs at least ${minLen === 2 ? "two" : minLen} [x, y] point(s) given its anchors`
          : "a pipe's points must be [x, y] pairs";
      }
      if (
        op.routing !== undefined &&
        op.routing !== "direct" &&
        op.routing !== "orthogonal"
      ) {
        return 'routing must be "direct" or "orthogonal"';
      }
      const id = op.id ?? genId(doc, "pipe");
      if (idTaken(doc, id)) return `id "${id}" is already taken`;
      const pipe: MimicPipe = { id, points: op.points };
      if (fromSet) pipe.from = op.from;
      if (toSet) pipe.to = op.to;
      if (op.routing === "orthogonal") pipe.routing = op.routing;
      (doc.pipes ??= []).push(pipe);
      break;
    }
    case "setPipePoints": {
      const pipe = (doc.pipes ?? []).find((p) => p.id === op.id);
      if (!pipe) return `no pipe "${op.id}"`;
      const minLen = minInteriorPoints(pipe.from !== undefined, pipe.to !== undefined);
      if (!validPointsList(op.points, minLen)) {
        return minLen > 0
          ? `a pipe needs at least ${minLen === 2 ? "two" : minLen} [x, y] point(s) given its anchors`
          : "a pipe's points must be [x, y] pairs";
      }
      pipe.points = op.points;
      break;
    }
    case "updatePipe": {
      const pipe = (doc.pipes ?? []).find((p) => p.id === op.id);
      if (!pipe) return `no pipe "${op.id}"`;
      const rename = op.patch.id;
      if (rename !== undefined) {
        if (typeof rename !== "string" || rename === "") return "id must be a non-empty string";
        if (rename !== pipe.id && idTaken(doc, rename)) return `id "${rename}" is already taken`;
      }
      if (op.patch.from !== undefined && op.patch.from !== null && !validAnchor(op.patch.from)) {
        return "from must be {equip, port}";
      }
      if (op.patch.to !== undefined && op.patch.to !== null && !validAnchor(op.patch.to)) {
        return "to must be {equip, port}";
      }
      // Effective anchor state AFTER this patch — needed either way, to
      // validate `points` (whether this patch touches it or not) against
      // whatever the anchors end up being.
      const fromSet = op.patch.from !== undefined ? op.patch.from !== null : pipe.from !== undefined;
      const toSet = op.patch.to !== undefined ? op.patch.to !== null : pipe.to !== undefined;
      const minLen = minInteriorPoints(fromSet, toSet);
      if (op.patch.points !== undefined) {
        if (!validPointsList(op.patch.points, minLen)) {
          return minLen > 0
            ? `a pipe needs at least ${minLen === 2 ? "two" : minLen} [x, y] point(s) given its anchors`
            : "a pipe's points must be [x, y] pairs";
        }
      } else if (op.patch.from !== undefined || op.patch.to !== undefined) {
        // Anchors changing without a points patch: the EXISTING points must
        // still satisfy the new floor (e.g. clearing an anchor on a
        // 0-interior-point pipe needs a points patch in the same op/batch).
        if (pipe.points.length < minLen) {
          return `clearing this anchor needs at least ${minLen === 2 ? "two" : minLen} [x, y] point(s) — patch points in the same op`;
        }
      }
      if (
        op.patch.routing !== undefined &&
        op.patch.routing !== null &&
        op.patch.routing !== "direct" &&
        op.patch.routing !== "orthogonal"
      ) {
        return 'routing must be "direct" or "orthogonal"';
      }
      applyPatch(pipe as unknown as Record<string, unknown>, op.patch);
      break;
    }
    case "deletePipe": {
      const list = doc.pipes ?? [];
      if (!list.some((p) => p.id === op.id)) return `no pipe "${op.id}"`;
      doc.pipes = list.filter((p) => p.id !== op.id);
      break;
    }
    case "addLabel": {
      (doc.labels ??= []).push({ text: op.text, x: op.x, y: op.y });
      break;
    }
    case "updateLabel": {
      const l = (doc.labels ?? [])[op.index];
      if (!l) return `no label #${op.index}`;
      applyPatch(l as unknown as Record<string, unknown>, op.patch);
      if (typeof l.text !== "string") return "a label needs text";
      break;
    }
    case "deleteLabel": {
      const list = doc.labels ?? [];
      if (!list[op.index]) return `no label #${op.index}`;
      doc.labels = list.filter((_, i) => i !== op.index);
      break;
    }
    default:
      return `unknown op "${(op as { type?: string }).type}"`;
  }
  return undefined;
}
