// Identifier scanning and value formatting for inline live values.
// Deliberately free of any `vscode` import so it can be unit-tested in
// plain Node (see scan.test.ts, run by `npm test`).
//
// Ported from mini-scada's CodeMirror inline-values scanner.

// A resolved reference site: the accessor path as written (for the hover
// label), the offset just past it (where the chip attaches), and the value
// that path resolves to — the referenced child for a member/index access
// (RTU.VALUE → the atomic), not the whole struct.
export type Site = { path: string; end: number; value: unknown };

const NOT_FOUND = Symbol("not-found");

/**
 * Find references to live tags — a tag name optionally followed by member
 * (`.Field`) and index (`[3]`, `[2,3]`) accessors — resolving each to the
 * value it names. Skips `//` and `(* *)` comments and string literals.
 * Base-tag matching is case-insensitive (IEC identifiers are); member keys
 * match case-insensitively against the struct's fields.
 */
export function scanIdentifiers(
  text: string,
  values: ReadonlyMap<string, unknown>
): Site[] {
  const out: Site[] = [];
  const n = text.length;
  let i = 0;
  while (i < n) {
    const c = text[i];
    if (c === "/" && text[i + 1] === "/") {
      while (i < n && text[i] !== "\n") i++;
      continue;
    }
    if (c === "(" && text[i + 1] === "*") {
      i += 2;
      while (i < n - 1 && !(text[i] === "*" && text[i + 1] === ")")) i++;
      i = Math.min(i + 2, n);
      continue;
    }
    if (c === '"' || c === "'") {
      const q = c;
      i++;
      while (i < n && text[i] !== q && text[i] !== "\n") {
        if (text[i] === "\\" && i + 1 < n) {
          i += 2;
          continue;
        }
        i++;
      }
      if (i < n && text[i] === q) i++;
      continue;
    }
    if (isIdentStart(c)) {
      const start = i;
      i++;
      while (i < n && isIdentPart(text[i])) i++;
      const lower = text.slice(start, i).toLowerCase();
      if (!values.has(lower)) continue;

      // Walk member/index accessors, resolving into the value. Stop at the
      // first accessor that can't be resolved (or a non-accessor char), and
      // report the deepest resolved value at that point.
      let value = values.get(lower);
      let end = i;
      let j = i;
      while (j < n) {
        if (text[j] === ".") {
          let k = j + 1;
          if (!(k < n && isIdentStart(text[k]))) break;
          const ms = k;
          k++;
          while (k < n && isIdentPart(text[k])) k++;
          const resolved = resolveMember(value, text.slice(ms, k));
          if (resolved === NOT_FOUND) break;
          value = resolved;
          j = k;
          end = k;
        } else if (text[j] === "[") {
          const parsed = parseIndex(text, j);
          if (!parsed) break;
          let v: unknown = value;
          for (const idx of parsed.indices) {
            const r = resolveIndex(v, idx);
            if (r === NOT_FOUND) {
              v = NOT_FOUND;
              break;
            }
            v = r;
          }
          if (v === NOT_FOUND) break;
          value = v;
          j = parsed.end;
          end = parsed.end;
        } else {
          break;
        }
      }
      out.push({ path: text.slice(start, end), end, value });
      i = end;
      continue;
    }
    i++;
  }
  return out;
}

/** resolveMember reads a struct field by name, case-insensitively. */
function resolveMember(value: unknown, member: string): unknown | typeof NOT_FOUND {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return NOT_FOUND;
  const obj = value as Record<string, unknown>;
  if (member in obj) return obj[member];
  const lower = member.toLowerCase();
  for (const key of Object.keys(obj)) {
    if (key.toLowerCase() === lower) return obj[key];
  }
  return NOT_FOUND;
}

/** resolveIndex reads an array element. */
function resolveIndex(value: unknown, idx: number): unknown | typeof NOT_FOUND {
  if (!Array.isArray(value) || idx < 0 || idx >= value.length) return NOT_FOUND;
  return value[idx];
}

/** parseIndex reads a `[N]` or `[N,M]` subscript starting at `[`. */
function parseIndex(text: string, at: number): { indices: number[]; end: number } | undefined {
  let k = at + 1;
  const indices: number[] = [];
  for (;;) {
    const start = k;
    while (k < text.length && text[k] >= "0" && text[k] <= "9") k++;
    if (k === start) return undefined;
    indices.push(parseInt(text.slice(start, k), 10));
    if (text[k] === ",") {
      k++;
      continue;
    }
    break;
  }
  if (text[k] !== "]") return undefined;
  return { indices, end: k + 1 };
}

function isIdentStart(c: string): boolean {
  return (c >= "A" && c <= "Z") || (c >= "a" && c <= "z") || c === "_";
}

function isIdentPart(c: string): boolean {
  return isIdentStart(c) || (c >= "0" && c <= "9");
}

/**
 * Compact value rendering: 59.887482 → 59.887, booleans → TRUE/FALSE.
 * Compound values (UDT structs, arrays) render as a quiet size hint —
 * `{…}` / `[4]` — the full breakdown lives in the hover (formatValueHover).
 */
export function formatValue(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "number") {
    if (Number.isInteger(v)) return String(v);
    const abs = Math.abs(v);
    if (abs !== 0 && (abs < 1e-3 || abs >= 1e6)) return v.toExponential(2);
    return v.toFixed(3);
  }
  if (typeof v === "boolean") return v ? "TRUE" : "FALSE";
  if (typeof v === "string") {
    const s = v.length > 32 ? v.slice(0, 29) + "…" : v;
    return JSON.stringify(s);
  }
  if (Array.isArray(v)) return `[${v.length}]`;
  if (typeof v === "object") return "{…}";
  return String(v);
}

/** Hover rendering caps so a 173-member AOI doesn't flood the tooltip. */
const HOVER_MAX_LINES = 40;
const HOVER_MAX_ELEMS = 10;

/**
 * Multi-line breakdown of a value for the hover, in the spirit of how
 * TypeScript expands a type on hover:
 *
 *	{
 *	  AI: -4.000
 *	  hhalm_timer: {
 *	    PRE: 1200000
 *	    ...
 *	  }
 *	}
 *
 * Scalars pass through formatValue; long arrays elide after HOVER_MAX_ELEMS
 * elements and the whole rendering elides after HOVER_MAX_LINES lines.
 */
export function formatValueHover(v: unknown): string {
  const lines: string[] = [];
  build(v, "", "", lines);
  if (lines.length > HOVER_MAX_LINES) {
    const kept = lines.slice(0, HOVER_MAX_LINES);
    kept.push(`… (${lines.length - HOVER_MAX_LINES} more lines)`);
    return kept.join("\n");
  }
  return lines.join("\n");
}

function build(v: unknown, label: string, indent: string, out: string[]): void {
  const prefix = label === "" ? indent : `${indent}${label}: `;
  if (Array.isArray(v)) {
    out.push(prefix + "[");
    const n = Math.min(v.length, HOVER_MAX_ELEMS);
    for (let i = 0; i < n; i++) {
      build(v[i], `[${i}]`, indent + "  ", out);
    }
    if (v.length > n) out.push(`${indent}  … (${v.length - n} more elements)`);
    out.push(indent + "]");
    return;
  }
  if (v !== null && typeof v === "object") {
    out.push(prefix + "{");
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      build(val, k, indent + "  ", out);
    }
    out.push(indent + "}");
    return;
  }
  out.push(prefix + formatValue(v));
}
