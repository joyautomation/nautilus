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

