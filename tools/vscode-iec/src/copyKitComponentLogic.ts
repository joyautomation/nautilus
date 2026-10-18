// The pure part of "Copy Kit Component to Project…" (copyKitComponent.ts):
// turning one of the kit's shipped `.svelte` sources into a component of
// the project's own. No vscode import — unit-tested in
// copyKitComponent.test.ts.
//
// Why a copy, not a wrapper: the ask is "start from the kit's Tank and
// make it mine" — the SVG, the colours, the graduations. A wrapper can
// only add around the kit's drawing; a copy IS the drawing, in src/lib,
// compiled by the project like any custom component, previewed and
// port-edited like one. The kit ships its components as real .svelte
// files (svelte-package), so the copy is the same source its author
// wrote, with one mechanical change: the kit's RELATIVE imports
// (`../color.js`, `./TankGlyph.svelte`) become imports of the package,
// which exports those helpers and components from its root.

export const KIT_PACKAGE = "@joyautomation/nautilus-hmi";

/** The names the kit's root index (dist/index.js) exports: `export {
 * default as X } from`, `export { a, b as c } from`, and plain
 * `export function f` / `export const x`. What a copied component may
 * import from the package. */
export function parseIndexExports(indexJs: string): Set<string> {
  const out = new Set<string>();
  const braced = /export\s*\{([^}]*)\}/g;
  let m: RegExpExecArray | null;
  while ((m = braced.exec(indexJs))) {
    for (const part of m[1].split(",")) {
      const spec = part.trim();
      if (!spec) continue;
      const as = /^(?:default|[\w$]+)\s+as\s+([\w$]+)$/.exec(spec);
      out.add(as ? as[1] : spec);
    }
  }
  const plain = /export\s+(?:async\s+)?(?:function|const|let|class)\s+([\w$]+)/g;
  while ((m = plain.exec(indexJs))) out.add(m[1]);
  return out;
}

export type Rewrite = {
  text: string;
  /** Imports left as they were because the kit doesn't export what they
   * name — the user has to copy or reimplement those. */
  unresolved: string[];
};

const IMPORT_RE = /^([ \t]*)import\s+(type\s+)?([^'"]+?)\s+from\s+(['"])(\.\.?\/[^'"]+)\4\s*;?[ \t]*$/gm;
const SIDE_EFFECT_RE = /^[ \t]*import\s+['"](\.\.?\/[^'"]+)['"]\s*;?[ \t]*$/gm;

/** Rewrite the copied source's relative imports to the kit package. A
 * default import of a sibling component (`import Glyph from
 * './TankGlyph.svelte'`) becomes a named import of that component
 * (`import { TankGlyph as Glyph } from pkg`) — svelte-package exports
 * every component by its file name. Named imports (`{ tempColor }`) are
 * rewritten when every VALUE name is exported; `import type` and inline
 * `type X` specifiers are rewritten unconditionally (erased at compile
 * time, and the index.d.ts carries the types). Namespace and side-effect
 * imports stay, listed as unresolved. */
export function rewriteKitImports(source: string, exported: ReadonlySet<string>, pkg = KIT_PACKAGE): Rewrite {
  const unresolved: string[] = [];
  let text = source.replace(IMPORT_RE, (line, indent: string, typeKw: string | undefined, clause: string, _q, spec: string) => {
    const c = clause.trim();
    if (typeKw) return `${indent}import type ${c} from '${pkg}';`;
    if (c.startsWith("*")) {
      unresolved.push(line.trim());
      return line;
    }
    const names: string[] = [];
    let ok = true;
    const braceAt = c.indexOf("{");
    const defaultPart = (braceAt === -1 ? c : c.slice(0, braceAt)).replace(/,\s*$/, "").trim();
    if (defaultPart) {
      const base = spec.split("/").pop()!.replace(/\.svelte$/, "");
      if (!exported.has(base)) ok = false;
      else names.push(base === defaultPart ? base : `${base} as ${defaultPart}`);
    }
    if (braceAt !== -1) {
      const inner = c.slice(braceAt + 1, c.lastIndexOf("}"));
      for (const part of inner.split(",")) {
        const spec1 = part.trim().replace(/\s+/g, " ");
        if (!spec1) continue;
        // An inline `type X` specifier is erased at compile time and
        // typed by the package's index.d.ts — never a reason to refuse.
        const isType = /^type\s/.test(spec1);
        const orig = spec1.split(/\s+as\s+/)[0].replace(/^type\s+/, "").trim();
        if (!isType && !exported.has(orig)) ok = false;
        names.push(spec1);
      }
    }
    if (!ok || names.length === 0) {
      unresolved.push(line.trim());
      return line;
    }
    return `${indent}import { ${names.join(", ")} } from '${pkg}';`;
  });
  text = text.replace(SIDE_EFFECT_RE, (line) => {
    unresolved.push(line.trim());
    return line;
  });
  return { text, unresolved };
}

/** The comment that heads the copy: where it came from, that it is now the
 * project's to change. Placed inside the component's first <script> so a
 * bare-markup component (no script) gets one added. */
export function copyHeader(from: string, newName: string, kitVersion: string, pkg = KIT_PACKAGE): string {
  return [
    `// ${newName}: ${from}.svelte copied from ${pkg}@${kitVersion} by "nautilus: Copy`,
    `// Kit Component to Project…" — now this project's own component: change`,
    `// the drawing, the props, the colours; the preview pane beside it shows`,
    `// the result as you type. Register it on a page with`,
    `// <Mimic registry={{ ${newName} }}> and point equipment at it by name.`,
  ].join("\n");
}

/** Insert `header` at the top of the component's first script block, or
 * as a new script block when the component has none. */
export function withHeader(source: string, header: string): string {
  const m = /<script\b[^>]*>\r?\n?/.exec(source);
  if (m) {
    const at = m.index + m[0].length;
    // Indent like the line that follows (the kit's scripts are tabbed).
    const indent = /^[ \t]*/.exec(source.slice(at))?.[0] ?? "";
    const indented = header
      .split("\n")
      .map((l) => indent + l)
      .join("\n");
    return source.slice(0, at) + indented + "\n" + source.slice(at);
  }
  return `<script lang="ts">\n${header}\n</script>\n\n${source}`;
}

/** Why `name` can't be the copy's name, or null when it can. PascalCase
 * (a Svelte component / a mimic `component` reference), not a kit
 * built-in the editor renders from the kit for that name, not already in
 * the project. */
export function validateNewComponentName(
  name: string,
  builtins: readonly string[],
  existing: readonly string[]
): string | null {
  const n = name.trim();
  if (!/^[A-Z][A-Za-z0-9]*$/.test(n)) return "PascalCase, letters and digits only (MyTank)";
  if (builtins.includes(n)) return `${n} is a kit built-in — the mimic editor renders the kit's own ${n} for that name; pick another (My${n})`;
  if (existing.includes(n)) return `${n}.svelte already exists in src/lib`;
  return null;
}
