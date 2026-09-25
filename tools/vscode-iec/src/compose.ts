// Run `naut compose` for online edits and "open block source". The pure
// half (the contract, parsing, overrides) is composeCli.ts.

import { execFile } from "node:child_process";
import * as vscode from "vscode";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing, minCliVersionNeeded } from "./cli";
import { composeArgs, ComposeResult, overridesFor, parseComposeOutput } from "./composeCli";

export type { ComposeResult, ComposedProgram } from "./composeCli";

/** Compose the project `target` (a file or directory on disk) belongs to,
 * exactly as the controller would compile it, with unsaved editor buffers
 * winning over the disk. */
export function nautCompose(target: vscode.Uri): Promise<{ ok: ComposeResult } | { error: string; tooOld?: boolean }> {
  const cli = cliCommand();
  const overrides = overridesFor(
    vscode.workspace.textDocuments.map((d) => ({
      fsPath: d.uri.fsPath,
      text: d.getText(),
      dirty: d.isDirty,
      isFile: d.uri.scheme === "file",
    }))
  );
  return new Promise((resolve) => {
    const child = execFile(cli, composeArgs(target.fsPath), cliExecOptions(), (err, stdout, stderr) => {
      if (isMissing(err)) return resolve({ error: cliMissingMessage(cli) });
      resolve(parseComposeOutput(err, String(stdout), String(stderr), minCliVersionNeeded()));
    });
    // A CLI that exits before reading stdin (one too old to know `compose`)
    // closes the pipe; that is reported through the exit, not as a crash.
    child.stdin?.on("error", () => undefined);
    child.stdin?.end(JSON.stringify(overrides));
  });
}
