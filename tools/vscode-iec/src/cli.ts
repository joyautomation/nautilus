// The one place the extension decides which naut binary to run: the
// language server, the diagram editors, and the acceptance tests all go
// through cliCommand(). See cliResolve.ts for why a bare name is searched
// for rather than handed to execFile.

import * as fs from "node:fs";
import * as os from "node:os";
import * as vscode from "vscode";
import { isExecutableFile, resolveCli, Resolved } from "./cliResolve";

const INSTALL_URL = "https://github.com/joyautomation/nautilus#getting-started";
const GO_INSTALL = "go install github.com/joyautomation/nautilus/cmd/naut@latest";

function configured(): string {
  return vscode.workspace.getConfiguration("nautilus").get<string>("cliPath") || "naut";
}

// Only a hit is cached: a miss is re-searched on the next call, so installing
// the CLI mid-session starts working without a reload.
let cache: { key: string; resolved: Resolved } | undefined;

export function resolveCliNow(): Resolved {
  const key = configured();
  if (cache?.key === key) return cache.resolved;
  const resolved = resolveCli(key, {
    env: process.env,
    platform: process.platform,
    home: os.homedir(),
    isExecutable: isExecutableFile,
  });
  if (resolved.found) cache = { key, resolved };
  return resolved;
}

/** The command to spawn for the nautilus CLI. */
export function cliCommand(): string {
  return resolveCliNow().command;
}

/** A Flatpak VS Code runs in a sandbox that can't execute host binaries, so
 * no amount of PATH fixing helps — the user needs to be told that instead. */
export function inFlatpak(): boolean {
  return !!process.env.FLATPAK_ID || fs.existsSync("/.flatpak-info");
}

/** One-paragraph explanation for a webview error banner. */
export function cliMissingMessage(cli: string): string {
  if (inFlatpak()) {
    return (
      `Couldn't run "${cli}": this VS Code is a Flatpak, and its sandbox can't run programs ` +
      "installed on the host. Use VS Code from a .deb/.rpm/tarball, or point nautilus.cliPath " +
      "at a binary inside the sandbox."
    );
  }
  return (
    `Couldn't find the nautilus CLI ("${cli}"). If it's installed, set nautilus.cliPath to its ` +
    `full path; VS Code started from a desktop launcher doesn't see a PATH set in your shell ` +
    `profile. To install: ${GO_INSTALL}, or see ${INSTALL_URL}`
  );
}

/** The spawn error was "no such program" (as opposed to the CLI failing). */
export function isMissing(err: unknown): boolean {
  const code = (err as NodeJS.ErrnoException | null)?.code;
  return code === "ENOENT" || code === "EACCES";
}

/** The actionable toast: find the binary, read the install steps, or copy
 * the go install line. Fire-and-forget — never await a notification. */
export function showCliMissing(cli: string, why?: string): void {
  const searched = resolveCliNow().searched;
  if (!why && searched.length) {
    const log = channel();
    log.appendLine(`nautilus CLI "${cli}" not found. Looked in:`);
    for (const d of searched) log.appendLine(`  ${d}`);
  }
  const detail = why ? ` (${why})` : "";
  const msg = inFlatpak()
    ? `nautilus: VS Code is running as a Flatpak and can't run the nautilus CLI from the host${detail}. ` +
      "Diagnostics and diagrams need VS Code installed from a .deb, .rpm, or tarball."
    : `nautilus: couldn't ${why ? "start" : "find"} the nautilus CLI${detail}. If it's installed, point the ` +
      "extension at it; a VS Code started from a desktop launcher doesn't see the PATH your shell sets.";
  const LOCATE = "Locate naut…";
  const INSTALL = "Install steps";
  const COPY = "Copy go install";
  const actions = inFlatpak() ? [INSTALL] : [LOCATE, INSTALL, COPY];
  void vscode.window.showWarningMessage(msg, ...actions).then(async (pick) => {
    if (pick === LOCATE) {
      const picked = await vscode.window.showOpenDialog({
        canSelectMany: false,
        openLabel: "Use this naut",
        title: "Locate the naut binary (the nautilus CLI)",
      });
      if (picked?.[0]) {
        // Global, not workspace: where the binary lives is a fact about this
        // machine, not about the project checked into git. The config change
        // restarts the language server.
        await vscode.workspace
          .getConfiguration("nautilus")
          .update("cliPath", picked[0].fsPath, vscode.ConfigurationTarget.Global);
      }
    } else if (pick === INSTALL) {
      void vscode.env.openExternal(vscode.Uri.parse(INSTALL_URL));
    } else if (pick === COPY) {
      void vscode.env.clipboard.writeText(GO_INSTALL);
    }
  });
}

let out: vscode.OutputChannel | undefined;
function channel(): vscode.OutputChannel {
  out ??= vscode.window.createOutputChannel("nautilus");
  return out;
}
