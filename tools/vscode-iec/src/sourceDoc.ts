// How a diagram preview finds the TextDocument it shows.
//
// A preview panel remembers its source by URI. `workspace.textDocuments`
// only lists documents VS Code currently holds open — close the last text
// editor on the file and it drops out, so a lookup there comes back empty
// and every gesture in the preview (edit, undo, redo, save) was silently
// ignored. `workspace.openTextDocument(uri)` re-opens the model WITHOUT
// showing an editor (and returns the live one when it's already open, dirty
// buffer included), so the lookup falls back to it.
//
// Kept vscode-free so it runs under `node --test`; diagramKeys.ts binds it
// to the real workspace.

export type HasUri = { uri: { toString(): string } };

/** The open document whose URI string is `key`, else whatever `reopen`
 * loads, else undefined (no URI tracked, or the file is gone). */
export async function resolveSourceDoc<D extends HasUri>(
  key: string | undefined,
  open: readonly D[],
  reopen: () => PromiseLike<D>
): Promise<D | undefined> {
  if (!key) return undefined;
  const hit = open.find((d) => d.uri.toString() === key);
  if (hit) return hit;
  try {
    return await reopen();
  } catch {
    return undefined;
  }
}

/** Runs async jobs one at a time, in arrival order: a preview's messages
 * used to be handled synchronously, and a lookup that now may await must
 * not let a later edit overtake an earlier one. A failed job doesn't stop
 * the ones after it. */
export function serialQueue(): (job: () => Promise<unknown> | unknown) => Promise<void> {
  let tail: Promise<unknown> = Promise.resolve();
  return (job) => {
    const next = tail.then(job).catch(() => undefined);
    tail = next;
    return next.then(() => undefined);
  };
}
