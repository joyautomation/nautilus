// What a 3D scene's palette lists (docs/design/spatial-hmi.md §3d) — the
// pure half, ahead of the editor that shows it (item 2). A kind is a
// built-in, or a `kinds` entry of the open *.scene.json, and each entry
// says how it is defined: a glTF (`model`, §3c), a Svelte component file
// (`component`) or an assembly of kinds (`assembly`); an entry with none of
// those only declares a contract for a kind the app registers itself. No
// vscode import here, so this is unit-testable with `node --test` like
// mimicComponentIndex.ts.
//
// Discovery of component files the document does not name yet follows the
// mimic's `{Name}.svelte` discovery, with one difference: a 3D kind's file
// is recognised by the `kind` export of its `<script module>`, so a
// workspace full of ordinary Svelte components lists only the ones that
// define a kind. isKindComponent is that recognition, over the file's text.

/** The built-in kinds hmi-3d ships. Mirror of hmi-3d's BUILTIN_CONTRACT
 * (registry.ts) and internal/scene's Builtin — the hmi-3d contract test
 * reads the schema, and TestBuiltinMatchesTheDataKinds holds the Go side
 * to models/kinds.json; this list is held to hmi-3d by sceneKinds.test.ts. */
export const BUILTIN_KINDS: ReadonlyArray<{ name: string; type: string; members: string[] }> = [
  { name: "tank", type: "Tank", members: ["Level", "TempC"] },
  { name: "pump", type: "Motor", members: ["Running", "Fault", "Speed"] },
  { name: "valve", type: "Valve", members: ["Pos", "Cmd"] },
];

export type KindDefinedBy = "builtin" | "model" | "component" | "assembly" | "declared";

export type PaletteKind = {
  name: string;
  definedBy: KindDefinedBy;
  /** The UDT a node of this kind binds, when known. */
  type?: string;
  /** The members the kind reads: the entry's, plus what its drives, status
   * template and assembly name. */
  members: string[];
  /** `component`: the file, relative to the scene; `model`: the glTF. */
  source?: string;
  /** An assembly's part count, for the palette's caption. */
  parts?: number;
};

type Json = Record<string, unknown>;
const isObj = (v: unknown): v is Json => typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);

const TEMPLATE_RE = /\{([A-Za-z_][A-Za-z0-9_]*)(?::\d)?(?:\?[^:}]*:[^}]*)?\}/g;

/** Every member a `kinds` entry reads, the union hmi-3d's kindMembers and
 * internal/scene's EffectiveKinds compute: `members`, the drives' binds,
 * the status template's fields, and an assembly's part tags and inner
 * ref roots. Order: members first, then in document order, deduplicated. */
export function entryMembers(entry: unknown): string[] {
  const out: string[] = [];
  const add = (m: unknown) => {
    if (typeof m !== "string") return;
    const root = m.replace(/^!/, "").split(".")[0];
    if (root && !out.includes(root)) out.push(root);
  };
  if (!isObj(entry)) return out;
  if (Array.isArray(entry.members)) for (const m of entry.members) add(m);
  if (Array.isArray(entry.drive))
    for (const d of entry.drive) {
      if (!isObj(d)) continue;
      for (const ch of ["spin", "turn", "scale", "tint", "emissive", "visible"]) {
        const c = d[ch];
        if (!isObj(c)) continue;
        // Num channels carry their bind inside revPerS / deg / to; bool channels carry it directly.
        const inner = c.revPerS ?? c.deg ?? c.to;
        add(isObj(inner) ? inner.bind : c.bind);
      }
    }
  if (typeof entry.status === "string") for (const m of entry.status.matchAll(TEMPLATE_RE)) add(m[1]);
  if (isObj(entry.assembly)) {
    if (Array.isArray(entry.assembly.nodes))
      for (const n of entry.assembly.nodes) {
        if (!isObj(n)) continue;
        add(n.tag);
        if (isObj(n.bind)) for (const ref of Object.values(n.bind)) add(ref);
      }
    if (Array.isArray(entry.assembly.pipes))
      for (const p of entry.assembly.pipes) if (isObj(p) && isObj(p.bind)) add(p.bind.flowing);
  }
  return out;
}

/** The palette's list for a parsed scene document: every built-in (a
 * `kinds` entry under a built-in's name re-points or re-models it, never
 * adds a second row), then every other `kinds` entry, sorted by name. A
 * malformed document (no object, `kinds` not an object) lists the
 * built-ins alone — the palette never disappears because a file is
 * mid-edit. */
export function paletteKinds(doc: unknown): PaletteKind[] {
  const kinds = isObj(doc) && isObj(doc.kinds) ? doc.kinds : {};
  const out: PaletteKind[] = [];
  for (const b of BUILTIN_KINDS) {
    const entry = kinds[b.name];
    const e = isObj(entry) ? entry : undefined;
    const extra = entryMembers(e).filter((m) => !b.members.includes(m));
    out.push({
      name: b.name,
      definedBy: e && str(e.model) ? "model" : "builtin",
      type: (e && str(e.type)) ?? b.type,
      members: [...b.members, ...extra],
      ...(e && str(e.model) ? { source: str(e.model) } : {}),
    });
  }
  const custom = Object.keys(kinds)
    .filter((k) => k && !BUILTIN_KINDS.some((b) => b.name === k))
    .sort();
  for (const name of custom) {
    const e = kinds[name];
    if (!isObj(e)) {
      out.push({ name, definedBy: "declared", members: [] });
      continue;
    }
    const definedBy: KindDefinedBy = str(e.model) ? "model" : str(e.component) ? "component" : isObj(e.assembly) ? "assembly" : "declared";
    const row: PaletteKind = { name, definedBy, type: str(e.type), members: entryMembers(e) };
    if (definedBy === "model") row.source = str(e.model);
    if (definedBy === "component") row.source = str(e.component);
    if (definedBy === "assembly") row.parts = Array.isArray((e.assembly as Json).nodes) ? ((e.assembly as Json).nodes as unknown[]).length : 0;
    out.push(row);
  }
  return out;
}

/** Does this Svelte file define a 3D kind? True when a `<script module>`
 * (or the legacy `context="module"`) exports `kind` — the marker
 * docs/design/spatial-hmi.md §3d fixes, checked over the text without
 * compiling anything. An ordinary component, a mimic user component, a
 * route file: false. */
export function isKindComponent(text: string): boolean {
  const re = /<script\b[^>]*\b(?:module\b|context\s*=\s*["']module["'])[^>]*>([\s\S]*?)<\/script>/gi;
  for (const m of text.matchAll(re)) {
    // Comments do not export anything.
    const code = m[1].replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "");
    if (/\bexport\s+(?:const|let|var|function)\s+kind\b/.test(code)) return true;
    if (/\bexport\s*\{[^}]*\bkind\b[^}]*\}/.test(code)) return true;
  }
  return false;
}
