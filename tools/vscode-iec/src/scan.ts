// Identifier scanning and value formatting for inline live values.
// Deliberately free of any `vscode` import so it can be unit-tested in
// plain Node (see scan.test.ts, run by `npm test`).
//
// Ported from mini-scada's CodeMirror inline-values scanner.

export type Site = { lowerName: string; end: number };

/**
 * Find identifiers whose lowercased name is a key of `values`, skipping
 * `//` line comments, `(* *)` block comments, and 'string' / "string"
 * literals. Returns the offset just past each identifier — where the
 * value chip attaches. Matching is case-insensitive (IEC identifiers are
 * case-insensitive; the runtime keys tags by declared casing).
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
      if (values.has(lower)) out.push({ lowerName: lower, end: i });
      continue;
    }
    i++;
  }
  return out;
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
