// "nautilus: Create Project…" — wraps `naut new <name> --no-input --template
// <template>` for the Getting Started walkthrough, so a first run needs no
// terminal.
//
// This is a thin wrapper, not a reimplementation: `--no-input` is the CLI's
// own non-interactive path (the same one CI/docker one-liners use), so this
// command supplies the same answers the interactive form would ask for
// through a couple of quick picks, then runs the CLI exactly as documented.
// It's worth having because `naut new` is cheap (it only writes files and
// `git init`s) and fully scriptable — there's no reason a first-time user
// needs a terminal open before they've seen anything run.
//
// The reusable pure logic (name validation, the template list, argv
// building) lives in newProjectLogic.ts, which has no vscode import and is
// plain-Node testable.

import { execFile } from "node:child_process";
import * as vscode from "vscode";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing } from "./cli";
import { NEW_PROJECT_TEMPLATES, newProjectArgs, validateProjectName } from "./newProjectLogic";

/** `naut new` itself is fast (file writes + `git init`), but give it real
 * room on a slow disk or an antivirus scanner in the way. */
export const NEW_PROJECT_TIMEOUT_MS = 30_000;

export function registerNewProjectCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nautilus.newProject", async () => {
    const parent = await pickParentFolder();
    if (!parent) return;

    const name = await vscode.window.showInputBox({
      title: "nautilus: Create Project",
      prompt: "Directory and program name for the new controller",
      placeHolder: "water-plant",
      validateInput: validateProjectName,
    });
    if (!name) return;

    const template = await vscode.window.showQuickPick(NEW_PROJECT_TEMPLATES, {
      title: "nautilus: Create Project — template",
      placeHolder: "Demo is the fastest way to see everything working",
    });
    if (!template) return;

    await createProject(parent, name.trim(), template.template);
  });
}

async function pickParentFolder(): Promise<vscode.Uri | undefined> {
  const picked = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    openLabel: "Create project here",
    title: "nautilus: choose a parent folder for the new project",
  });
  return picked?.[0];
}

async function createProject(parent: vscode.Uri, name: string, template: string): Promise<void> {
  const cli = cliCommand();
  const args = newProjectArgs(name, template);
  const projectUri = vscode.Uri.joinPath(parent, name);

  const ok = await vscode.window.withProgress(
    { location: vscode.ProgressLocation.Notification, title: `nautilus: creating ${name}…` },
    () =>
      new Promise<boolean>((resolve) => {
        execFile(
          cli,
          args,
          cliExecOptions({ cwd: parent.fsPath, timeoutMs: NEW_PROJECT_TIMEOUT_MS }),
          (err, _stdout, stderr) => {
            if (err) {
              if (isMissing(err)) {
                void vscode.window.showErrorMessage(cliMissingMessage(cli));
              } else {
                void vscode.window.showErrorMessage(
                  `nautilus: naut new failed — ${stderr.trim() || err.message}`
                );
              }
              resolve(false);
              return;
            }
            resolve(true);
          }
        );
      })
  );
  if (!ok) return;

  const OPEN = "Open in New Window";
  const OPEN_HERE = "Open Here";
  const actions = vscode.workspace.workspaceFolders?.length ? [OPEN, OPEN_HERE] : [OPEN_HERE, OPEN];
  const pick = await vscode.window.showInformationMessage(`nautilus: created ${name}/`, ...actions);
  if (pick === OPEN) {
    await vscode.commands.executeCommand("vscode.openFolder", projectUri, { forceNewWindow: true });
  } else if (pick === OPEN_HERE) {
    await vscode.commands.executeCommand("vscode.openFolder", projectUri, { forceNewWindow: false });
  }
}
