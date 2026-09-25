// The one place the extension decides which naut binary to run: the
// language server, the diagram editors, and the acceptance tests all go
// through cliCommand(). See cliResolve.ts for why a bare name is searched
// for rather than handed to execFile, and cliInstall.ts for the one-click
// install this file wires into commands and prompts.

import { execFile, ExecFileOptionsWithStringEncoding } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as vscode from "vscode";
import { isExecutableFile, resolveCli, Resolved } from "./cliResolve";
import { checkMinVersion, installLatestCli, parseCliVersionOutput, RELEASES_PAGE } from "./cliInstall";

const INSTALL_URL = "https://github.com/joyautomation/nautilus#getting-started";
const GO_INSTALL = "go install github.com/joyautomation/nautilus/cmd/naut@latest";

/** Every short-lived CLI call (graph, edit, version) gets this long. A hung
 * CLI would otherwise stall a diagram's edit queue for good. */
export const CLI_TIMEOUT_MS = 15_000;

/** execFile options for a CLI call: a timeout and room for big diagram JSON. */
export function cliExecOptions(o: { timeoutMs?: number; maxBuffer?: number; cwd?: string } = {}): ExecFileOptionsWithStringEncoding {
  return {
    encoding: "utf8",
    timeout: o.timeoutMs ?? CLI_TIMEOUT_MS,
    maxBuffer: o.maxBuffer ?? 16 * 1024 * 1024,
    ...(o.cwd ? { cwd: o.cwd } : {}),
  };
}

// ── setup ──────────────────────────────────────────────────────────────────

let managedBinDir: string | undefined;
let minCliVersion = "0.0.0";
let state: vscode.Memento | undefined;

/** Call first thing in activate(): where the managed install lives, and the
 * oldest CLI this build of the extension works with (package.json's
 * nautilusCli.minVersion). */
export function initCli(context: vscode.ExtensionContext): void {
  managedBinDir = path.join(context.globalStorageUri.fsPath, "bin");
  minCliVersion = (context.extension.packageJSON as { nautilusCli?: { minVersion?: string } }).nautilusCli?.minVersion ?? "0.0.0";
  state = context.globalState;
  cache = undefined;
}

/** The oldest naut this extension works with (nautilusCli.minVersion). */
export function minCliVersionNeeded(): string {
  return minCliVersion;
}

/** The binary the "Install naut" command writes. */
export function managedCliPath(): string | undefined {
  return managedBinDir && path.join(managedBinDir, process.platform === "win32" ? "naut.exe" : "naut");
}

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
    managedDir: managedBinDir,
  });
  if (resolved.found) cache = { key, resolved };
  return resolved;
}

/** The command to spawn for the nautilus CLI. */
export function cliCommand(): string {
  return resolveCliNow().command;
}

function samePath(a: string, b: string | undefined): boolean {
  if (!b) return false;
  const norm = (p: string) => (process.platform === "win32" ? path.resolve(p).toLowerCase() : path.resolve(p));
  return norm(a) === norm(b);
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
      "installed on the host. Run \"nautilus: Install or Update the naut CLI\" to put a copy " +
      "inside the sandbox, or use VS Code from a .deb/.rpm/tarball."
    );
  }
  return (
    `Couldn't find the nautilus CLI ("${cli}"). Run "nautilus: Install or Update the naut CLI" ` +
    `from the Command Palette to install it. If it's already installed, set nautilus.cliPath to its ` +
    `full path; VS Code started from a desktop launcher doesn't see a PATH set in your shell profile.`
  );
}

/** The spawn error was "no such program" (as opposed to the CLI failing). */
export function isMissing(err: unknown): boolean {
  const code = (err as NodeJS.ErrnoException | null)?.code;
  return code === "ENOENT" || code === "EACCES";
}

// ── the missing-CLI prompt ─────────────────────────────────────────────────

/** The actionable toast: install it, find the binary, or read the install
 * steps. Fire-and-forget — never await a notification. */
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
      "Install a copy inside the sandbox, or use VS Code installed from a .deb, .rpm, or tarball."
    : `nautilus: couldn't ${why ? "start" : "find"} the nautilus CLI${detail}. Diagnostics and the diagram ` +
      "editors need it. Install it in one click, or point the extension at the naut you already have.";
  const INSTALL = "Install naut";
  const LOCATE = "Locate naut…";
  const STEPS = "Install steps";
  const actions = inFlatpak() ? [INSTALL, STEPS] : [INSTALL, LOCATE, STEPS];
  void vscode.window.showWarningMessage(msg, ...actions).then(async (pick) => {
    if (pick === INSTALL) {
      void vscode.commands.executeCommand("nautilus.installCli");
    } else if (pick === LOCATE) {
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
    } else if (pick === STEPS) {
      void vscode.env.openExternal(vscode.Uri.parse(INSTALL_URL));
    }
  });
}

// ── install / update ───────────────────────────────────────────────────────

let installing: Promise<void> | undefined;

/** "nautilus: Install or Update the naut CLI": the latest CLI release,
 * verified, into the extension's global storage; then the language server
 * restarts on it. */
export function installCliCommand(): Promise<void> {
  installing ??= runInstall().finally(() => (installing = undefined));
  return installing;
}

async function runInstall(): Promise<void> {
  const dest = managedBinDir;
  if (!dest) return;
  const log = channel();
  let installed: { version: string; path: string };
  try {
    installed = await vscode.window.withProgress(
      { location: vscode.ProgressLocation.Notification, title: "nautilus", cancellable: true },
      async (progress, token) => {
        const abort = new AbortController();
        token.onCancellationRequested(() => abort.abort());
        let at = 0;
        return installLatestCli({
          binDir: dest,
          signal: abort.signal,
          report: (message, percent) => {
            const increment = percent !== undefined && percent > at ? percent - at : undefined;
            if (percent !== undefined) at = Math.max(at, percent);
            progress.report({ message, increment });
            log.appendLine(`[install] ${message}`);
          },
        });
      }
    );
  } catch (err) {
    const why = err instanceof Error ? err.message : String(err);
    log.appendLine(`[install] failed: ${why}`);
    if (/cancel/i.test(why)) return;
    const OPEN = "Open Releases page";
    const pick = await vscode.window.showErrorMessage(
      `nautilus: couldn't install naut: ${why}. You can download it from the Releases page instead ` +
        "(behind a proxy, check VS Code's http.proxy setting).",
      OPEN
    );
    if (pick === OPEN) void vscode.env.openExternal(vscode.Uri.parse(RELEASES_PAGE));
    return;
  }

  log.appendLine(`[install] naut ${installed.version} → ${installed.path}`);
  cache = undefined;
  checked.clear();
  const now = resolveCliNow();
  void vscode.commands.executeCommand("nautilus.restartLanguageServer");
  if (samePath(now.command, installed.path)) {
    void vscode.window.showInformationMessage(`nautilus: naut ${installed.version} installed.`);
    return;
  }
  // Something earlier in the search order (PATH, or an explicit cliPath)
  // still wins. Say so rather than silently not using the new binary.
  const USE = "Use the installed one";
  const pick = await vscode.window.showInformationMessage(
    `nautilus: naut ${installed.version} installed, but the extension is still using ${now.command} ` +
      `(${howInstalled(now.command)}), which comes first.`,
    USE
  );
  if (pick === USE) {
    await vscode.workspace
      .getConfiguration("nautilus")
      .update("cliPath", installed.path, vscode.ConfigurationTarget.Global);
  }
}

/** A guess at who put a binary where it is, for "update it the same way". */
function howInstalled(cmd: string): string {
  if (samePath(cmd, managedCliPath())) return "installed by this extension";
  if (configured().includes("/") || configured().includes("\\")) return "set by nautilus.cliPath";
  const dir = path.dirname(cmd);
  const goDirs = [process.env.GOBIN, ...(process.env.GOPATH ?? "").split(path.delimiter).map((p) => p && path.join(p, "bin")), path.join(os.homedir(), "go", "bin")];
  if (goDirs.some((d) => d && samePath(dir, d))) return "a go install";
  return "found on PATH or in a well-known directory";
}

/** `howInstalled` as a sentence about `cmd`: "…/naut was found on PATH or in
 * a well-known directory", not "…/naut is found on PATH…". */
function installedSentence(cmd: string, how: string): string {
  if (how === "a go install") return `${cmd} was installed with go install`;
  if (how.startsWith("found ")) return `${cmd} was ${how}`;
  return `${cmd} is ${how}`;
}

// ── version ────────────────────────────────────────────────────────────────

/** `naut version`'s answer, or undefined when it didn't give one in time. */
export function cliVersion(cmd: string): Promise<string | undefined> {
  return new Promise((resolve) => {
    execFile(cmd, ["version"], cliExecOptions({ timeoutMs: 5_000, maxBuffer: 64 * 1024 }), (err, stdout) =>
      resolve(err ? undefined : parseCliVersionOutput(String(stdout)))
    );
  });
}

// Per session: a language-server restart doesn't re-ask the same binary.
const checked = new Set<string>();

/** Log the resolved CLI and its version, and warn when it is older than
 * this extension needs. Fire-and-forget. */
export async function checkCliVersion(): Promise<void> {
  const cli = resolveCliNow();
  if (!cli.found || checked.has(cli.command)) return;
  checked.add(cli.command);
  const log = channel();
  const version = await cliVersion(cli.command);
  log.appendLine(`nautilus CLI: ${cli.command} (${howInstalled(cli.command)}), version ${version ?? "unknown"}; this extension needs ≥ ${minCliVersion}`);
  if (!version) return;
  const verdict = checkMinVersion(version, minCliVersion);
  if (verdict.kind === "dev") {
    log.appendLine(`  "${version}" isn't a release version (a local build?); assuming it's current.`);
    return;
  }
  if (verdict.kind === "ok") return;

  const key = `${version}<${minCliVersion}`;
  if (state?.get<string>("nautilus.cli.skipVersionWarning") === key) return;
  const managed = samePath(cli.command, managedCliPath());
  const UPDATE = "Update naut";
  const SKIP = "Don't show again for this version";
  const pick = await vscode.window.showWarningMessage(
    `nautilus: naut ${version} is older than this extension needs (${minCliVersion}); some features may fail.`,
    UPDATE,
    SKIP
  );
  if (pick === SKIP) {
    await state?.update("nautilus.cli.skipVersionWarning", key);
  } else if (pick === UPDATE) {
    if (managed) {
      void installCliCommand();
      return;
    }
    const how = howInstalled(cli.command);
    const INSTALL = "Install the extension's own copy";
    const COPY = "Copy go install";
    const actions = how === "a go install" ? [INSTALL, COPY] : [INSTALL];
    const next = await vscode.window.showInformationMessage(
      `nautilus: ${installedSentence(cli.command, how)}. Update it the way it was installed` +
        (how === "a go install" ? ` (${GO_INSTALL})` : "") +
        ", or install a copy the extension manages and keeps up to date.",
      ...actions
    );
    if (next === INSTALL) void installCliCommand();
    else if (next === COPY) void vscode.env.clipboard.writeText(GO_INSTALL);
  }
}

/** "nautilus: Show CLI Info": which naut, from where, what version. */
export async function showCliInfo(): Promise<void> {
  const cli = resolveCliNow();
  const log = channel();
  if (!cli.found) {
    showCliMissing(cli.command);
    return;
  }
  const version = (await cliVersion(cli.command)) ?? "unknown";
  log.appendLine(`nautilus CLI: ${cli.command} (${howInstalled(cli.command)}), version ${version}; needs ≥ ${minCliVersion}`);
  const UPDATE = "Install or Update";
  const LOG = "Show Log";
  const pick = await vscode.window.showInformationMessage(
    `nautilus: naut ${version} at ${cli.command} (${howInstalled(cli.command)}). This extension needs ${minCliVersion} or newer.`,
    UPDATE,
    LOG
  );
  if (pick === UPDATE) void installCliCommand();
  else if (pick === LOG) log.show();
}

let out: vscode.OutputChannel | undefined;
function channel(): vscode.OutputChannel {
  out ??= vscode.window.createOutputChannel("nautilus");
  return out;
}
