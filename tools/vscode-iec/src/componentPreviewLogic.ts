// The preview's pure part — no vscode import, so it's unit-testable with
// `node --test` over out/ (componentPreview.test.ts), like mimicOps.ts next
// to mimicEditor.ts.

/** Pure: the Svelte buffers the preview compiles from — every DIRTY .svelte
 * document (the previewed component may import one that's mid-edit too),
 * plus the previewed file itself whether dirty or not (an unsaved new file
 * has no disk copy at all). Keyed by fsPath for userComponentBuild's
 * `sources`. */
export function previewSources(
  docs: readonly { fsPath: string; isSvelte: boolean; isDirty: boolean; text: () => string }[],
  targetFsPath: string
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const d of docs) {
    if (!d.isSvelte) continue;
    if (d.isDirty || d.fsPath === targetFsPath) out[d.fsPath] = d.text();
  }
  return out;
}


/** Pure: what the preview command opens for the active file. A
 * `{Name}.svelte` component previews itself; a `{Name}.component.json`
 * sidecar previews the component it describes — a built-in (Tank, Pump,
 * …: `builtins`) straight from the kit's registry, anything else from its
 * `.svelte` source (the caller locates it); null for every other file
 * (a `+page.svelte` route, an unrelated .json). */
export function previewTargetForPath(
  fsPath: string,
  builtins: readonly string[],
  componentNameFromSveltePath: (p: string) => string | null,
  componentNameFromFilename: (basename: string) => string | null
): { kind: "svelte" | "builtin" | "custom"; name: string } | null {
  const svelte = componentNameFromSveltePath(fsPath);
  if (svelte) return { kind: "svelte", name: svelte };
  const base = fsPath.split(/[\\/]/).pop() ?? "";
  const name = componentNameFromFilename(base);
  if (!name) return null;
  return { kind: builtins.includes(name) ? "builtin" : "custom", name };
}

/** Pure: the props a component must be given — the names in its
 * `let { … } = $props()` that have no default (`...rest` aside). The
 * preview names them when the component draws nothing, since a component
 * given none of them usually can't (`x`, `y` and `status` undefined make
 * every path `M NaN …`). Read off the text, not the compiler: it has to
 * answer for a buffer that does not compile yet. */
export function propsWithoutDefault(source: string): string[] {
  const call = source.search(/=\s*\$props\s*\(/);
  if (call < 0) return [];
  // The pattern is the last `let {` before the call whose braces close
  // before it (a type annotation `}: { … }` may sit in between).
  let open = -1;
  for (const m of source.slice(0, call).matchAll(/\b(?:let|const)\s*\{/g)) open = m.index + m[0].length - 1;
  if (open < 0) return [];
  const close = matchingBrace(source, open);
  if (close < 0 || close > call) return [];
  // Between the pattern and `= $props()`: nothing, or a type annotation.
  const between = source.slice(close + 1, call);
  if (!/^\s*(:(?![\s\S]*\b(?:let|const|var|function)\b)[\s\S]*)?$/.test(between)) return [];
  const names: string[] = [];
  for (const entry of splitTopLevel(source.slice(open + 1, close), ",")) {
    const text = entry.trim();
    if (text === "" || text.startsWith("...")) continue;
    const [target, ...def] = splitTopLevel(text, "=");
    if (def.length > 0) continue;
    // `name` or `name: alias` — the prop is the name before the colon.
    const name = target.split(":")[0].trim();
    if (/^[A-Za-z_$][\w$]*$/.test(name)) names.push(name);
  }
  return names;
}

const CLOSERS: Record<string, string> = { "{": "}", "(": ")", "[": "]" };

/** Skips a string or template literal or a comment starting at `i`;
 * returns the index after it, or `i` when there is none there. */
function skipQuiet(s: string, i: number): number {
  const c = s[i];
  if (c === '"' || c === "'" || c === "`") {
    for (let j = i + 1; j < s.length; j++) {
      if (s[j] === "\\") j++;
      else if (s[j] === c) return j + 1;
    }
    return s.length;
  }
  if (c === "/" && s[i + 1] === "/") {
    const end = s.indexOf("\n", i);
    return end < 0 ? s.length : end;
  }
  if (c === "/" && s[i + 1] === "*") {
    const end = s.indexOf("*/", i + 2);
    return end < 0 ? s.length : end + 2;
  }
  return i;
}

function matchingBrace(s: string, open: number): number {
  let depth = 0;
  for (let i = open; i < s.length; i++) {
    const quiet = skipQuiet(s, i);
    if (quiet !== i) {
      i = quiet - 1;
      continue;
    }
    if (s[i] in CLOSERS) depth++;
    else if (s[i] === "}" || s[i] === ")" || s[i] === "]") {
      if (--depth === 0) return i;
    }
  }
  return -1;
}

/** Splits on `sep` outside brackets, strings and comments. `=` does not
 * split inside `=>`, `==` or `<=`/`>=`/`!=`. */
function splitTopLevel(s: string, sep: "," | "="): string[] {
  const out: string[] = [];
  let depth = 0;
  let from = 0;
  for (let i = 0; i < s.length; i++) {
    const quiet = skipQuiet(s, i);
    if (quiet !== i) {
      i = quiet - 1;
      continue;
    }
    const c = s[i];
    if (c in CLOSERS) depth++;
    else if (c === "}" || c === ")" || c === "]") depth--;
    else if (c === sep && depth === 0) {
      if (sep === "=" && (s[i + 1] === ">" || s[i + 1] === "=" || "=!<>".includes(s[i - 1] ?? " "))) continue;
      out.push(s.slice(from, i));
      from = i + 1;
    }
  }
  out.push(s.slice(from));
  return out;
}
