// Cross-reference from a diagram element (#218) and tag descriptions on it
// (#216): the pure rules, unit-tested without vscode. diagramXref.ts is the
// host side; webview-ui/src/xref.svelte.ts the webview side.
//
// A diagram element (a ladder contact or coil, an FBD chip or block pin, an
// SFC action association or step) carries the identifier it draws and the
// source lines it came from — a ladder rung's RUNG..last line, an FBD
// node's statement, an SFC step's block. The webview posts those; the host
// finds the identifier in the diagram's own TEXT (the .ld/.fbd/.sfc file)
// and asks the language server for references at that position — the
// server's references already report diagram files on their own lines.

/** What the webview posts for "Find All References" on an element. */
export type XrefMessage = { type: "xref"; name: string; line?: number; endLine?: number };

export function isXrefMessage(msg: unknown): msg is XrefMessage {
  const m = msg as { type?: unknown; name?: unknown; line?: unknown; endLine?: unknown } | null;
  return (
    !!m &&
    m.type === "xref" &&
    typeof m.name === "string" &&
    m.name.trim() !== "" &&
    (m.line === undefined || typeof m.line === "number") &&
    (m.endLine === undefined || typeof m.endLine === "number")
  );
}

/** The identifier path an element's label names: `+M1_StartPB` (an edge
 * contact) is M1_StartPB, `Levels[i]` is Levels, `t1.Q` stays t1.Q (the
 * member, so the references are Q OF t1). Undefined when nothing in it is
 * an identifier (a literal, an expression). */
export function xrefPath(label: string): string[] | undefined {
  let s = label.trim().replace(/^[+\-/]\s*/, "").replace(/^NOT\s+/i, "");
  // Drop index expressions, innermost first, so `a[b[1]].c` → `a.c`.
  for (let prev = ""; prev !== s; ) {
    prev = s;
    s = s.replace(/\[[^[\]]*\]/g, "");
  }
  const parts = s.split(".").map((p) => p.trim());
  if (parts.length === 0 || !parts.every((p) => /^[A-Za-z_][A-Za-z0-9_]*$/.test(p))) return undefined;
  return parts;
}

/** The text with every comment, pragma and string literal blanked to
 * spaces — same length, newlines kept — so a search never lands inside
 * one (a rung comment naming the tag, an `(* @layout *)` block). */
export function maskNonCode(text: string): string {
  const out = text.split("");
  const blank = (from: number, to: number) => {
    for (let k = from; k < to && k < out.length; k++) if (out[k] !== "\n") out[k] = " ";
  };
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    const n = text[i + 1];
    if (c === "(" && n === "*") {
      const end = text.indexOf("*)", i + 2);
      const stop = end < 0 ? text.length : end + 2;
      blank(i, stop);
      i = stop;
    } else if (c === "/" && n === "/") {
      const end = text.indexOf("\n", i);
      const stop = end < 0 ? text.length : end;
      blank(i, stop);
      i = stop;
    } else if (c === "'" || c === '"') {
      let j = i + 1;
      while (j < text.length && text[j] !== c && text[j] !== "\n") j += text[j] === "$" ? 2 : 1;
      const stop = Math.min(j + 1, text.length);
      blank(i, stop);
      i = stop;
    } else {
      i++;
    }
  }
  return out.join("");
}

/** 0-based, like vscode.Position. */
export type TextPos = { line: number; character: number };

/** Where `label` occurs in the diagram's source text: the first whole-word
 * occurrence inside the element's own lines (1-based, inclusive) when they
 * are known and contain it, else the first in the file. For a member path
 * the position is on its LAST segment — the language server answers "Q of
 * t1" there. Case-insensitive, as IEC identifiers are. */
export function findIdentifier(
  text: string,
  label: string,
  hint?: { line?: number; endLine?: number }
): TextPos | undefined {
  const path = xrefPath(label);
  if (!path) return undefined;
  const esc = path.map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  // Each segment may carry an index between it and the next dot.
  const body = esc.join(String.raw`(?:\s*\[[^\]\n]*\])*\s*\.\s*`);
  const re = new RegExp(String.raw`(?<![A-Za-z0-9_.])` + body + String.raw`(?![A-Za-z0-9_])`, "gi");
  // A rung's NAME is a label, not a variable: `RUNG m1:` never answers
  // for the instance m1 the rung calls.
  const masked = maskNonCode(text).replace(/^(\s*RUNG\s+)([A-Za-z_]\w*)/gim, (_, kw: string, name: string) => kw + " ".repeat(name.length));
  const lineStarts = [0];
  for (let k = 0; k < masked.length; k++) if (masked[k] === "\n") lineStarts.push(k + 1);
  const toPos = (off: number): TextPos => {
    let lo = 0;
    let hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= off) lo = mid;
      else hi = mid - 1;
    }
    return { line: lo, character: off - lineStarts[lo] };
  };
  const lastSeg = new RegExp(esc[esc.length - 1] + "$", "i");
  let first: TextPos | undefined;
  for (let m = re.exec(masked); m; m = re.exec(masked)) {
    const at = m.index + m[0].search(lastSeg);
    const pos = toPos(at);
    first ??= pos;
    if (hint?.line !== undefined) {
      const lo = hint.line - 1;
      const hi = (hint.endLine ?? hint.line) - 1;
      if (pos.line >= lo && pos.line <= hi) return pos;
    } else {
      return pos;
    }
  }
  return first;
}

/** Descriptions keyed for the webview: lower-cased names (IEC is case-
 * insensitive), empty ones dropped. */
export function normalizeDescriptions(raw: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  const src = (raw as { descriptions?: unknown } | null)?.descriptions;
  if (!src || typeof src !== "object") return out;
  for (const [k, v] of Object.entries(src as Record<string, unknown>)) {
    if (typeof v === "string" && v.trim() !== "") out[k.toLowerCase()] = v.trim();
  }
  return out;
}

/** The diagram languages whose files have descriptions to show. */
export function describesLanguage(languageId: string): boolean {
  return languageId === "iec-ld" || languageId === "iec-fbd" || languageId === "iec-sfc";
}
