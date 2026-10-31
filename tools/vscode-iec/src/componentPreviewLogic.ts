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
