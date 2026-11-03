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
import * as os from "node:os";
import * as vscode from "vscode";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing } from "./cli";
import {
  NEW_PROJECT_LANGUAGES,
  NEW_PROJECT_TEMPLATES,
  newProjectArgs,
  parentFolderProblem,
  resolveParentInput,
  templateTakesLanguage,
  validateProjectName,
} from "./newProjectLogic";

/** `naut new` itself is fast (file writes + `git init`), but give it real
 * room on a slow disk or an antivirus scanner in the way. */
export const NEW_PROJECT_TIMEOUT_MS = 30_000;

export function registerNewProjectCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nautilus.newProject", async () => {
    // The steps are one quick input that never closes between them: each step
    // is shown BEFORE the previous one is disposed (showing a quick input
    // while another is up swaps it in place). Closing it between steps would
    // hand the focus back to whatever had it before the command ran; on a
    // fresh window that is the Welcome page, whose step media is a webview
    // that takes the focus a moment later, so the next box opened without the
    // keyboard focus and typing went to the webview (#405, #443). Every step
    // still ignores focus-out, so a webview that does grab focus cannot close
    // one.
    const parent = await pickParentFolder();
    if (!parent) return;

    const name = await nextStep(parent.input, pickName);
    if (!name) return;

    const template = await nextStep(name.input, () =>
      pickOne(NEW_PROJECT_TEMPLATES, {
        title: "nautilus: Create Project — template",
        placeholder: "Demo is the fastest way to see everything working",
      })
    );
    if (!template) return;

    let language: string | undefined;
    let last: vscode.QuickInput = template.input;
    if (templateTakesLanguage(template.value.template)) {
      const picked = await nextStep(template.input, () =>
        pickOne(NEW_PROJECT_LANGUAGES, {
          title: "nautilus: Create Project — program language",
          placeholder: "The language of the first program (you can add the others later)",
        })
      );
      if (!picked) return;
      language = picked.value.language;
      last = picked.input;
    }
    last.dispose();

    await createProject(parent.value, name.value.trim(), template.value.template, language);
  });
}

/** One step's answer, with its quick input still up: the next step is shown
 * before this one is disposed (see registerNewProjectCommand). */
interface Step<T> {
  value: T;
  input: vscode.QuickInput;
}

/** Runs `step`, then disposes `prev`, which the step's own input has
 * already replaced on screen. A step that was cancelled has disposed its
 * own input, so nothing is left up. */
async function nextStep<T>(prev: vscode.QuickInput, step: () => Promise<Step<T> | undefined>,
): Promise<Step<T> | undefined> {
  const shown = step();
  prev.dispose();
  return shown;
}

/** Shows `input`; `wire` hooks its events and calls `answer` once the step
 * has its value, which resolves with the input still up for the next step
 * (until then the step keeps waiting). Hiding it (Escape)
 * resolves undefined and disposes it. Once answered, the hide that showing
 * the next step causes is ignored. */
function runStep<T>(input: vscode.QuickInput,
  wire: (answer: (value: T) => void) => void,
  keepOnHide?: () => boolean,
): Promise<Step<T> | undefined> {
  input.ignoreFocusOut = true;
  return new Promise((resolve) => {
    let done = false;
    wire((value) => {
      if (done) return;
      done = true;
      resolve({ value, input });
    });
    input.onDidHide(() => {
      if (done || keepOnHide?.()) return;
      done = true;
      input.dispose();
      resolve(undefined);
    });
    input.show();
  });
}

function pickName(): Promise<Step<string> | undefined> {
  const box = vscode.window.createInputBox();
  box.title = "nautilus: Create Project";
  box.prompt = "Directory and program name for the new controller";
  box.placeholder = "water-plant";
  return runStep(box, (answer) => {
    box.onDidChangeValue((text) => { box.validationMessage = validateProjectName(text); });
    box.onDidAccept(() => {
      const problem = validateProjectName(box.value);
      box.validationMessage = problem;
      if (!problem) answer(box.value);
    });
  });
}

function pickOne<T extends vscode.QuickPickItem>(items: T[], opts: { title: string; placeholder: string },
): Promise<Step<T> | undefined> {
  const pick = vscode.window.createQuickPick<T>();
  pick.title = opts.title;
  pick.placeholder = opts.placeholder;
  pick.items = items;
  return runStep(pick, (answer) => {
    pick.onDidAccept(() => {
      const chosen = pick.selectedItems[0] ?? pick.activeItems[0];
      if (chosen) answer(chosen);
    });
  });
}

/** The parent-folder step is an input box (default folder prefilled, Enter
 * accepts, a folder button opens the system picker), NOT showOpenDialog:
 * with `files.simpleDialog.enable` (VS Code for the web, serve-web, remote
 * setups) the open dialog is a plain filter input where Enter navigates
 * instead of confirming "Create project here" (#86), and the extension has
 * no hook into that. An input box behaves the same in every dialog mode. */
function pickParentFolder(): Promise<Step<vscode.Uri> | undefined> {
  const home = os.homedir();
  const base = vscode.workspace.workspaceFolders?.[0]?.uri.scheme === "file"
    ? vscode.workspace.workspaceFolders[0].uri.fsPath
    : home;
  const browse: vscode.QuickInputButton = {
    iconPath: new vscode.ThemeIcon("folder-opened"),
    tooltip: "Browse for a folder…",
  };
  const problemFor = async (text: string): Promise<string | undefined> => {
    const resolved = resolveParentInput(text, home, base);
    if (!resolved) return "choose a folder";
    let kind: "dir" | "file" | "missing";
    try {
      const st = await vscode.workspace.fs.stat(vscode.Uri.file(resolved));
      kind = st.type & vscode.FileType.Directory ? "dir" : "file";
    } catch {
      kind = "missing";
    }
    return parentFolderProblem(kind);
  };

  const box = vscode.window.createInputBox();
  box.title = "nautilus: Create Project — parent folder";
  box.prompt = "Folder to create the project in (Enter to accept, or browse)";
  box.value = base;
  box.buttons = [browse];
  // The simple file dialog (files.simpleDialog.enable: the web, serve-web,
  // remote) is itself a quick input: Browse hides this box under it, and that
  // hide must not cancel the flow (#528). The box comes back if the dialog
  // is cancelled; a picked folder answers the step.
  let browsing = false;
  return runStep(box, (answer) => {
    let seq = 0;
    box.onDidChangeValue(async (text) => {
      const mine = ++seq;
      const problem = await problemFor(text);
      if (mine === seq) box.validationMessage = problem;
    });
    box.onDidAccept(async () => {
      const problem = await problemFor(box.value);
      if (problem) {
        box.validationMessage = problem;
        return;
      }
      answer(vscode.Uri.file(resolveParentInput(box.value, home, base)!));
    });
    box.onDidTriggerButton(async () => {
      if (browsing) return; // one dialog at a time
      browsing = true;
      let picked: vscode.Uri[] | undefined;
      try {
        picked = await vscode.window.showOpenDialog({
          canSelectFiles: false,
          canSelectFolders: true,
          canSelectMany: false,
          defaultUri: vscode.Uri.file(resolveParentInput(box.value, home, base) ?? base),
          openLabel: "Create project here",
          title: "nautilus: choose a parent folder for the new project",
        });
      } catch {
        // A dialog that fails (a remote file system error) is a cancel: the
        // box comes back, rather than the flow waiting on a hidden box.
        picked = undefined;
      } finally {
        browsing = false;
      }
      if (picked?.[0]) answer(picked[0]);
      else box.show(); // back from a cancelled simple dialog; a no-op if it stayed up
    });
  }, () => browsing);
}

async function createProject(parent: vscode.Uri, name: string, template: string,
  language?: string
): Promise<void> {
  const cli = cliCommand();
  const args = newProjectArgs(name, template, language);
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
