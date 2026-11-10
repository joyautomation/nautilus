// AGENTS.md, the guidance AI coding assistants read before writing a
// project's programs (#645). The extension never carries the text: it asks
// `naut agents -check -json` whether a project's copy is missing or stale,
// offers to fix it, and runs `naut agents` to do so, so the guidance always
// matches the naut that checks the code. An older naut without the command
// means no offer at all.
//
// The offer is made for a nautilus.yaml at the root of a workspace folder
// (the project someone opened), once per project and kind of offer. The
// palette command "nautilus: Add or Update AGENTS.md" reaches any project in
// the workspace, nested ones included.

import { execFile } from "node:child_process";
import * as path from "node:path";
import * as vscode from "vscode";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing } from "./cli";
import { agentsOffer, AgentsCheck, parseAgentsCheck, rememberKey, silenced } from "./agentsMdLogic";

function run(args: string[], cwd: string): Promise<{ code: number; stdout: string; stderr: string; missing: boolean }> {
  return new Promise((resolve) => {
    execFile(cliCommand(), args, cliExecOptions({ cwd }), (err, stdout, stderr) => {
      const code = err ? (typeof (err as { code?: unknown }).code === "number" ? ((err as { code: number }).code) : -1) : 0;
      resolve({ code, stdout: stdout ?? "", stderr: stderr ?? "", missing: !!err && isMissing(err) });
    });
  });
}

/** What `naut agents -check -json` says; `missing` when there is no naut
 * to ask (undefined check either way: an older naut has no answer). */
async function check(dir: string): Promise<{ c: AgentsCheck | undefined; missing: boolean }> {
  const r = await run(["agents", "-check", "-json", "."], dir);
  return { c: parseAgentsCheck(r.stdout), missing: r.missing };
}

function openAgentsMd(dir: string): void {
  void vscode.window.showTextDocument(vscode.Uri.file(path.join(dir, "AGENTS.md")));
}

/** Runs naut agents with `args` in `dir` and says what it did. */
async function apply(dir: string, args: string[]): Promise<boolean> {
  const r = await run([...args, "."], dir);
  if (r.missing) {
    void vscode.window.showErrorMessage(cliMissingMessage(cliCommand()));
    return false;
  }
  if (r.code !== 0) {
    void vscode.window.showErrorMessage(`nautilus: naut agents failed — ${(r.stderr || r.stdout).trim()}`);
    return false;
  }
  const said = r.stdout.trim().split(/\r?\n/).join(" · ");
  const open = "Open AGENTS.md";
  void vscode.window.showInformationMessage(`nautilus: ${said}`, open).then((pick) => {
    if (pick === open) openAgentsMd(dir);
  });
  return true;
}

/** Projects already offered this window session: one notification per
 * project, however many nautilus.yaml events follow, and "Not now" (or a
 * dismissed toast) lasts until the window reloads. */
const offered = new Set<string>();

async function offer(context: vscode.ExtensionContext, folder: vscode.WorkspaceFolder): Promise<void> {
  if (!vscode.workspace.getConfiguration("nautilus").get<boolean>("agentsMd.offer", true)) return;
  const dir = folder.uri.fsPath;
  const key = rememberKey(dir);
  if (offered.has(dir) || silenced(context.workspaceState.get<string>(key))) return;
  offered.add(dir);
  const { c } = await check(dir);
  if (!c) return;
  const o = agentsOffer(c, folder.name);
  if (!o) return;
  const notNow = "Not now";
  const never = "Never for this project";
  const pick = await vscode.window.showInformationMessage(o.message, o.action, notNow, never);
  if (pick === o.action) {
    if (o.args.length === 0) openAgentsMd(dir);
    else await apply(dir, o.args);
  } else if (pick === never) {
    await context.workspaceState.update(key, "never");
  }
}

/** Every folder holding a nautilus.yaml in the workspace. */
async function projectDirs(): Promise<string[]> {
  const found = await vscode.workspace.findFiles("**/nautilus.yaml", "**/node_modules/**", 200);
  return [...new Set(found.map((u) => path.dirname(u.fsPath)))].sort();
}

async function command(): Promise<void> {
  const dirs = await projectDirs();
  if (dirs.length === 0) {
    void vscode.window.showWarningMessage("nautilus: no nautilus.yaml in this workspace — AGENTS.md goes beside a project's manifest.");
    return;
  }
  let dir = dirs[0];
  if (dirs.length > 1) {
    const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
    const pick = await vscode.window.showQuickPick(
      dirs.map((d) => ({ label: path.relative(root, d) || path.basename(d), dir: d })),
      { placeHolder: "Which project?" }
    );
    if (!pick) return;
    dir = pick.dir;
  }
  const { c, missing } = await check(dir);
  if (missing) {
    void vscode.window.showErrorMessage(cliMissingMessage(cliCommand()));
    return;
  }
  if (!c) {
    void vscode.window.showErrorMessage("nautilus: this naut has no `naut agents` command — update naut (nautilus: Install or Update the naut CLI).");
    return;
  }
  if (c.agentsMd === "conflict") {
    const open = "Open AGENTS.md";
    const replace = "Replace the file";
    const pick = await vscode.window.showWarningMessage(
      "nautilus: AGENTS.md mixes nautilus's guidance with edits naut cannot separate (or its nautilus markers are broken). Move your own notes above or below the nautilus text and replace it, or replace the whole file now?",
      { modal: true },
      open,
      replace
    );
    if (pick === open) openAgentsMd(dir);
    else if (pick === replace) await apply(dir, ["agents", "-force"]);
    return;
  }
  if (c.agentsMd === "unmanaged") {
    const append = "Add below my text";
    const replace = "Replace the file";
    const pick = await vscode.window.showWarningMessage(
      "nautilus: AGENTS.md here was not written by naut. Add the nautilus guidance below its text, or replace the file?",
      { modal: true },
      append,
      replace
    );
    if (pick === append) await apply(dir, ["agents", "-append"]);
    else if (pick === replace) await apply(dir, ["agents", "-force"]);
    return;
  }
  await apply(dir, ["agents"]);
}

export function registerAgentsMd(context: vscode.ExtensionContext): void {
  context.subscriptions.push(vscode.commands.registerCommand("nautilus.agentsMd.update", command));
  const offerAll = () => {
    for (const folder of vscode.workspace.workspaceFolders ?? []) {
      void vscode.workspace.fs.stat(vscode.Uri.joinPath(folder.uri, "nautilus.yaml")).then(
        () => offer(context, folder),
        () => undefined
      );
    }
  };
  offerAll();
  const watcher = vscode.workspace.createFileSystemWatcher("**/nautilus.yaml");
  context.subscriptions.push(
    watcher,
    watcher.onDidCreate(offerAll),
    vscode.workspace.onDidChangeWorkspaceFolders(offerAll)
  );
}
