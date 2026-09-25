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
