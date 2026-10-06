// Declared tag types for the live-value surfaces (#246): an enumerated tag's
// value streams as its member's NAME ("Run"), which on its own reads exactly
// like a STRING. GET /api/meta says which tags (and program locals, and
// struct/FB members) are enumerations, with their members; this module turns
// that into a flat, case-insensitive lookup the inline pills, the Live Values
// panel, the Set/Force prompts and the diagram webviews share.
//
// No vscode import and only erasable TypeScript, so it runs under plain
// node:test here and the webview bundle imports it as it stands.

/** One named value of an enumerated type, as /api/meta lists it. */
export type EnumMember = { name: string; value: number };

/** A declared type, flattened: `t` its name (`REAL`, `Pump`, `Mode`), `e`
 * an enumeration's members in declaration order. */
export type FlatType = { t: string; e?: EnumMember[] };

/** typeKey(path) → FlatType. Keys are lowercased dotted paths with every
 * index collapsed to `[]` ("tbl[2].Mode" → "tbl[].mode"), since every
 * element of an array has the element's type. */
export type FlatTypes = Record<string, FlatType>;

/** The /api/meta shape this reads (server/tagtypes.go typeInfo). */
type MetaType = {
  type?: string;
  enum?: { name?: unknown; value?: unknown }[];
  members?: Record<string, MetaType>;
  elem?: MetaType;
};

/** The lookup key for a value path: lowercased, indexes collapsed. */
export function typeKey(path: string): string {
  return path.replace(/\[[^\]]*\]/g, "[]").toLowerCase();
}

/** Flatten /api/meta's `tags` and `locals` into one lookup. Tags win over a
 * same-named local, as they do in the merged value map. Anything malformed
 * is skipped: a controller older than #246 has no types and every value
 * renders as before. */
export function flattenMetaTypes(meta: unknown): FlatTypes {
  const out: FlatTypes = {};
  if (!meta || typeof meta !== "object") return out;
  const m = meta as { tags?: Record<string, MetaType>; locals?: Record<string, MetaType> };
  for (const group of [m.locals, m.tags]) {
    if (!group || typeof group !== "object") continue;
    for (const [name, info] of Object.entries(group)) addType(out, name.toLowerCase(), info, 0);
  }
  return out;
}

function addType(out: FlatTypes, key: string, info: MetaType | undefined, depth: number): void {
  if (!info || typeof info !== "object" || depth > 32) return;
  if (typeof info.type === "string" && info.type !== "") {
    const ft: FlatType = { t: info.type };
    if (Array.isArray(info.enum)) {
      ft.e = info.enum
        .filter((x) => x && typeof x.name === "string")
        .map((x) => ({ name: x.name as string, value: typeof x.value === "number" ? x.value : NaN }));
    }
    out[key] = ft;
  }
  if (info.members && typeof info.members === "object") {
    for (const [k, v] of Object.entries(info.members)) addType(out, `${key}.${k.toLowerCase()}`, v, depth + 1);
  }
  if (info.elem) addType(out, `${key}[]`, info.elem, depth + 1);
}

/** The type of the value at `path` ("Mode", "P101.State", "tbl[3].Mode"). */
export function typeFor(types: FlatTypes | undefined, path: string): FlatType | undefined {
  if (!types || !path) return undefined;
  return types[typeKey(path)];
}

/** True for an enumerated type. */
export function isEnum(ft: FlatType | undefined): ft is FlatType & { e: EnumMember[] } {
  return !!ft && Array.isArray(ft.e);
}

/** True for a STRING (or WSTRING, or STRING[n]) type. */
export function isStringType(ft: FlatType | undefined): boolean {
  return !!ft && !isEnum(ft) && /^W?STRING(\s*[[(]\s*\d+\s*[\])])?$/i.test(ft.t);
}

/** An enumerated value's display text — the member's name, bare (no
 * quotes: it is not a STRING) — or undefined when `v` is not an enumerated
 * value, so the caller renders it as it always has. An integer (a value
 * forced or converted in by number) shows as its member when one has it. */
export function enumText(v: unknown, ft: FlatType | undefined): string | undefined {
  if (!isEnum(ft)) return undefined;
  if (typeof v === "string") return v.length > 32 ? v.slice(0, 29) + "…" : v;
  if (typeof v === "number" && Number.isInteger(v)) {
    const m = ft.e.find((x) => x.value === v);
    return m ? m.name : undefined;
  }
  return undefined;
}

/** The type as a hover shows it: `Mode · enum` for an enumeration, else
 * the type's name. undefined when the type is unknown. */
export function typeLabel(ft: FlatType | undefined): string | undefined {
  if (!ft) return undefined;
  return isEnum(ft) ? `${ft.t} · enum` : ft.t;
}

/** One entry of the Set/Force member pick. */
export type EnumPick = { label: string; description: string; value: number; current?: boolean; alwaysShow?: boolean };

/** The enumeration's members as pick items — `Run`, `= 10` — the current
 * one marked. */
export function enumPickItems(ft: FlatType & { e: EnumMember[] }, current: unknown): EnumPick[] {
  const cur = enumText(current, ft);
  return ft.e.map((m) => ({
    label: m.name,
    description: `= ${m.value}${cur !== undefined && m.name.toLowerCase() === cur.toLowerCase() ? " · current" : ""}`,
    value: m.value,
    current: cur !== undefined && m.name.toLowerCase() === cur.toLowerCase(),
  }));
}

/** Parse what an operator typed into the member pick: a member's name
 * (case-insensitive, optionally `Mode#`-qualified, as IEC writes it) or an
 * integer — the value an enumerated tag takes over the API. undefined for
 * anything else. */
export function parseEnumWrite(raw: string, ft: FlatType & { e: EnumMember[] }): number | undefined {
  const s = raw.trim();
  if (s === "") return undefined;
  if (/^[+-]?\d+$/.test(s)) return Number(s);
  const hash = s.indexOf("#");
  const name = (hash >= 0 ? s.slice(hash + 1) : s).toLowerCase();
  if (hash >= 0 && s.slice(0, hash).toLowerCase() !== ft.t.toLowerCase()) return undefined;
  const m = ft.e.find((x) => x.name.toLowerCase() === name);
  return m ? m.value : undefined;
}

/** The extra row a typed entry adds to the pick when it is not simply one
 * of the listed names (an integer, or a `Mode#Run` form): what it sets. */
export function typedEnumPick(raw: string, ft: FlatType & { e: EnumMember[] }): EnumPick | undefined {
  const s = raw.trim();
  const v = parseEnumWrite(s, ft);
  if (v === undefined) return undefined;
  if (ft.e.some((m) => m.name.toLowerCase() === s.toLowerCase())) return undefined; // already a row
  const m = ft.e.find((x) => x.value === v);
  return { label: s, description: m ? `→ ${m.name}` : `integer, no member of ${ft.t} has it`, value: v, alwaysShow: true };
}

/** A written value as the confirmation shows it: the member's name when it
 * has one. */
export function enumValueName(v: number, ft: FlatType & { e: EnumMember[] }): string {
  return ft.e.find((x) => x.value === v)?.name ?? String(v);
}
