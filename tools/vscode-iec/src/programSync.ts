// Pure helpers for the online-edit feature (onlineEdit.ts) — vscode-free so
// they run under plain node:test (programSync.test.ts). The composition
// itself (which files are libraries and programs, the prelude, POU names)
// comes from the CLI — `naut compose`, see composeCli.ts; these only take a
// composed source apart again and compare it.

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

/** How the program file stands in the editor when Pull is about to replace
 * it: not loaded at all, loaded and matching disk, or loaded with unsaved
 * edits. */
export type PullDocState = "closed" | "clean" | "dirty";

/** The Pull confirmation and how the pulled text gets written — see
 * pullPlan. */
export type PullPlan = {
  /** The modal's text. */
  message: string;
  /** The button that goes ahead with the overwrite. */
  overwrite: string;
  /** "Show Diff" for a dirty buffer (open the buffer ↔ controller diff and
   * write nothing); undefined otherwise. */
  showDiff?: string;
  /** "disk": the file is not open — write it with workspace.fs. "buffer":
   * it is open — replace the whole buffer with a WorkspaceEdit (so the
   * editor and any diagram view show the pulled program, and Undo brings
   * the previous text back) and then save it, so disk and buffer agree and
   * the next Ctrl+S cannot write a stale buffer over the pull (#140). */
  write: "disk" | "buffer";
};

/** Decide Pull's confirmation and write path from the program file's editor
 * state — the replace-a-file-with-unsaved-changes convention other editors
 * follow: a dirty buffer is named in the confirmation, which says its edits
 * will be lost, and the pull goes through the buffer, never under it. */
export function pullPlan(programFile: string, doc: PullDocState): PullPlan {
  if (doc === "dirty") {
    return {
      message:
        `${programFile} has unsaved edits. Overwrite the editor and the file with the controller's program? ` +
        "Your edits will be lost.",
      overwrite: "Overwrite",
      showDiff: "Show Diff",
      write: "buffer",
    };
  }
  return {
    message: `Overwrite ${programFile} with the controller's program?`,
    overwrite: "Pull and overwrite",
    write: doc === "clean" ? "buffer" : "disk",
  };
}

// ── project library layout ──────────────────────────────────────────────
//
// Where a project's IEC files may live — the path half of internal/
// stproject's LibraryPaths/ProjectRoot, for features that only scan text
// (live-value instance discovery): its root PLUS every file under lib/, at
// any depth. Every other subdirectory is ignored. What joins a composition
// is the CLI's call (`naut compose`), never decided here.

/** The project subdirectory whose IEC files are all libraries. */
export const LIB_DIR = "lib";

/** Is a project-relative, slash-separated path under lib/? */
export function inLibDir(rel: string): boolean {
  return rel.startsWith(LIB_DIR + "/");
}

/** Is a project-relative path a library CANDIDATE — root-level, or under
 * lib/ (skipping dot-directories and node_modules there)? */
export function isLibraryCandidate(rel: string): boolean {
  const segs = rel.split("/");
  if (segs.length === 1) return true;
  if (segs[0] !== LIB_DIR) return false;
  return !segs.slice(1, -1).some((s) => s.startsWith(".") || s === "node_modules");
}

/** Sort project-relative paths the way Go's sort.Strings does (bytewise),
 * so the composed prelude matches the controller's byte for byte. */
export function sortPaths(paths: string[]): string[] {
  return [...paths].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}
