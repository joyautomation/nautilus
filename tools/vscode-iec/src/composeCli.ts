// The extension's side of `naut compose --json`: what the CLI prints, and
// how its answer (or its failure) becomes something online edits can use.
// vscode-free so it runs under plain node:test (composeCli.test.ts).
//
// Online edits (download, diff, pull, rollback, the sync status) need the
// exact source the controller would compile for a program: the project's
// library prelude — .st libraries verbatim, then .ld/.fbd libraries
// TRANSPILED, root and lib/ — followed by the program as written. The
// transpiling is the CLI's job, so the extension asks `naut compose` rather
// than re-implementing the rule (internal/stproject.Libraries is the single
// definition; the runtime, `naut check` and this command all use it).

/** One program file in the project root. */
export type ComposedProgram = {
  file: string; // project-relative (a root file)
  pou: string; // PROGRAM <Name>, "" if unnamed
  language: "st" | "fbd" | "ld" | "sfc";
  program: string; // the file's source, as written
};

/** `naut compose --json`'s output. */
export type ComposeResult = {
  root: string; // absolute project directory
  prelude: string; // the composed library prelude (Join(libraries, ""))
  libraries: string[]; // project-relative library paths, in prelude order
  programs: ComposedProgram[];
  // Set when a program file was named.
  file?: string;
  pou?: string;
  language?: string;
  program?: string;
  source?: string; // prelude + program: what a download sends
};

/** The command line for a composition of `target` (a program file, library
 * file, or project directory), with unsaved buffers on stdin. */
export function composeArgs(target: string): string[] {
  return ["compose", "--json", "--overrides", "-", target];
}

/** Did the CLI reject `compose` itself — a naut older than the extension
 * needs? (`nautilus: unknown command "compose"`, exit 2.) */
export function composeUnsupported(stderr: string): boolean {
  return /unknown command "compose"/.test(stderr);
}

/** The message to show when the CLI predates `naut compose`. */
export function composeTooOldMessage(minVersion: string): string {
  return (
    `nautilus: online edits need naut ${minVersion} or newer (this naut has no \`naut compose\`, ` +
    `which composes the program with the project's ladder/FBD libraries exactly as the controller does). ` +
    `Run "nautilus: Install or Update the naut CLI".`
  );
}

/** Turn the CLI's exit into a result or a one-line error. `missing` is the
 * spawn-failed-to-find-the-binary case (the caller has the right message). */
export function parseComposeOutput(
  exitError: unknown,
  stdout: string,
  stderr: string,
  minVersion: string
): { ok: ComposeResult } | { error: string; tooOld?: boolean } {
  if (!exitError) {
    try {
      const res = JSON.parse(stdout) as ComposeResult;
      if (typeof res.prelude === "string" && Array.isArray(res.programs)) return { ok: res };
    } catch {
      /* fall through */
    }
    return { error: "nautilus: naut compose gave no usable output" };
  }
  if (composeUnsupported(stderr)) return { error: composeTooOldMessage(minVersion), tooOld: true };
  const msg = stderr.trim().split("\n").filter((l) => l.trim()).pop();
  if (msg) return { error: "nautilus: " + msg.replace(/^naut compose:\s*/, "naut compose: ") };
  const killed = (exitError as { killed?: boolean }).killed;
  return { error: `nautilus: naut compose ${killed ? "timed out" : "failed"}` };
}

/** The unsaved buffers a composition should see, keyed by absolute file
 * path (`--overrides` accepts those and ignores any outside the project the
 * CLI resolves). Only dirty on-disk documents: a saved one reads the same
 * from disk. */
export function overridesFor(docs: { fsPath: string; text: string; dirty: boolean; isFile: boolean }[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const d of docs) if (d.dirty && d.isFile) out[d.fsPath] = d.text;
  return out;
}

// ── the status poll's composition cache ─────────────────────────────────
//
// The sync status polls the controller every few seconds; recomposing on
// every tick would spawn `naut compose` constantly for nothing. It recomposes
// only when an input to composition changed: which project the target
// resolves to, the set and mtime/size of the project's IEC files (root, lib/,
// and nautilus.yaml, which decides where a lib/ file's project root is), or
// an unsaved buffer's version. Explicit commands (download, diff, pull,
// rollback) always compose fresh and refresh the cache.

/** One project file as a cheap stat sweep sees it. */
export type FileStamp = { rel: string; mtime: number; size: number };

/** An open unsaved buffer, which wins over the disk in a composition. */
export type BufferStamp = { path: string; version: number };

/** Everything a composition depends on, as one comparable string.
 * Order-insensitive: the same inputs listed differently give the same key. */
export function compositionKey(target: string, cli: string, files: FileStamp[], dirty: BufferStamp[]): string {
  const f = [...files].sort((a, b) => (a.rel < b.rel ? -1 : a.rel > b.rel ? 1 : 0)).map((s) => `${s.rel}@${s.mtime}:${s.size}`);
  const d = [...dirty].sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)).map((b) => `${b.path}#${b.version}`);
  return JSON.stringify([target, cli, f, d]);
}

/** A failed composition is retried after this long even with an unchanged
 * key — the failure may be the CLI itself (updated in place), which no
 * project file records. */
export const COMPOSE_ERROR_TTL_MS = 30_000;

/** Can the status poll reuse `cached` for `key` at time `now`? */
export function reuseComposition(
  cached: { key: string; at: number; failed: boolean } | undefined,
  key: string,
  now: number
): boolean {
  if (!cached || cached.key !== key) return false;
  return !cached.failed || now - cached.at < COMPOSE_ERROR_TTL_MS;
}

// ── resolving Download/Pull's target when the active file is a library ──
//
// A library (a PROGRAM-less .st/.ld/.fbd file) has no program of its own to
// route by, but naut compose <program> already knows which program(s)
// instantiate its blocks — onlineEdit.ts asks the same question by reading
// the library's declared names and text-searching each program's own body,
// rather than refusing outright the way it used to (see PENDING.md finding
// "Download from a library file").

/** Every `FUNCTION_BLOCK`/`FUNCTION` name a library declares — the
 * candidates a consuming program's own source might instantiate or call. */
export function declaredBlockNames(librarySource: string): string[] {
  const names = new Set<string>();
  for (const m of librarySource.matchAll(/\b(?:FUNCTION_BLOCK|FUNCTION)\s+([A-Za-z_][A-Za-z0-9_]*)/g)) {
    names.add(m[1]);
  }
  return [...names];
}

/** Does `programBody` reference any of `blockNames` (a whole-word match —
 * "roc2 : RateOfChange(...)", "MotorStarter(...)", ...)? */
export function referencesAnyBlock(programBody: string, blockNames: string[]): boolean {
  return blockNames.some((n) => new RegExp(`\\b${n}\\b`).test(programBody));
}
