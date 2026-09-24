// Pure helpers for the online-edit feature (onlineEdit.ts) — vscode-free so
// they run under plain node:test (programSync.test.ts). The composition rule
// mirrors internal/stproject: sibling .st libraries (sorted by name) precede
// the program body, and the same prelude joins every program in a
// multi-program project.

/** The `PROGRAM <Name>` POU name of IEC source, "" if none. Programs on a
 * multi-task controller are addressed by this name. */
export function pouOf(src: string): string {
  const m = /^\s*PROGRAM\s+([A-Za-z_][A-Za-z0-9_]*)/m.exec(src);
  return m ? m[1] : "";
}

/** Recover the program body from composed source given the prelude Join
 * placed ahead of it — the TypeScript mirror of stproject.SplitProgram.
 * Returns undefined when the prelude isn't a prefix (the controller's
 * libraries don't match this project). */
export function splitProgram(composed: string, prelude: string): string | undefined {
  if (composed.startsWith(prelude)) return composed.slice(prelude.length);
  const trimmed = prelude.replace(/\n+$/, "");
  if (trimmed !== prelude && composed.startsWith(trimmed)) {
    return composed.slice(trimmed.length).replace(/^\n/, "");
  }
  return undefined;
}

/** The library prefix of a controller's composed source — what that task
 * believes the shared .st libraries say. A deployed source carries the
 * workspace prelude verbatim; an online edit may have rewritten it, so fall
 * back to peeling the task's known program body off the end, then to cutting
 * at the `PROGRAM` line every IEC language opens its program file with. A
 * source that defeats all three (fully diverged) is returned whole — the
 * diff then shows everything that task runs, which is the honest picture. */
export function controllerPrelude(source: string, wsPrelude: string, wsBody?: string): string {
  if (splitProgram(source, wsPrelude) !== undefined) return wsPrelude;
  if (wsBody) {
    const trimmed = wsBody.replace(/\n+$/, "");
    if (source.endsWith(wsBody)) return source.slice(0, source.length - wsBody.length);
    if (trimmed && source.endsWith(trimmed)) return source.slice(0, source.length - trimmed.length);
  }
  const m = /^[ \t]*PROGRAM\b/m.exec(source);
  if (m) return source.slice(0, m.index);
  return source;
}

/** Whitespace-insensitive comparison: embed order and blank lines differ
 * between a binary's embed composition and the editor's, but the logic
 * doesn't. */
export function normalize(src: string): string {
  return src
    .replace(/\r/g, "")
    .split("\n")
    .map((l) => l.replace(/\s+$/g, ""))
    .filter((l) => l.length > 0)
    .join("\n");
}

// ── online-edit confirmation wording ────────────────────────────────────
//
// Pure string builders for the modal confirmations onlineEdit.ts shows
// before it changes a running controller's program — factored out here (like
// the rest of this file) so the wording is unit-testable without vscode.

/** `<file> (<pou>)`, or just `<file>` when the POU name is unknown. */
function targetLabel(programFile: string, pou: string): string {
  return pou ? `${programFile} (${pou})` : programFile;
}

/** Modal text shown before download() PUTs the workspace program to a
 * running controller, replacing what it's currently executing. Names the
 * controller URL, the program file/POU being replaced, and the controller's
 * current program hash, so a controls engineer knows exactly what is about
 * to change on what may be a live process. */
export function downloadConfirmMessage(url: string, programFile: string, pou: string, hash: string): string {
  return `Download ${targetLabel(programFile, pou)} to the controller at ${url}, replacing its current program (${hash})?`;
}

/** Modal text shown before the "Force download" path (already itself a
 * confirmation, offered after a 409 conflict) overwrites a controller
 * program that changed since it was last read. Kept just as explicit about
 * the target as downloadConfirmMessage. */
export function forceDownloadConfirmMessage(url: string, programFile: string, pou: string, reason: string): string {
  return (
    `nautilus: controller program at ${url} changed under you — download ${targetLabel(programFile, pou)} ` +
    `anyway and overwrite it?${reason ? " " + reason : ""}`
  );
}

/** Modal text shown before rollback() undoes the last online edit on a
 * running controller. `pou` is "" when the target program's identity isn't
 * known (e.g. no program file in the workspace) — the message still names
 * the controller. */
export function rollbackConfirmMessage(url: string, pou: string): string {
  return `Roll back ${pou || "the program"} on ${url} to the previous program?`;
}
