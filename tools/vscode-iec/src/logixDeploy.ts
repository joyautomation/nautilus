// Deploy to a Logix controller from the editor: Push Online Edit and
// Download Project, the two things `naut logix deploy --online|--download`
// does, as commands and editor-title buttons. Nothing is reimplemented
// here: `naut logix serve --project` (the runtime URL the live values
// already follow) runs the deploy through logixd and streams its progress
// back from POST /api/logix/deploy (logix/facade/program.go). Live values
// keep coming from the same server throughout — an online edit changes no
// tags, so nothing the editor polls goes away.
//
// The pure half (wording, the NDJSON stream, the fallbacks' command lines)
// is logixDeployLogic.ts.

import * as path from "node:path";
import * as vscode from "vscode";
import { cliCommand } from "./cli";
import { notifyError, notifyInfo, notifyWarning } from "./testHooks";
import { OnlineEdit } from "./onlineEdit";
import { projectDirFor } from "./projectFiles";
import {
  declaresLogixTarget,
  DeployEvent,
  DeployMode,
  DOWNLOAD,
  DOWNLOAD_PROGRAM_MODE,
  doneSummary,
  downloadConfirmMessage,
  LogixInfo,
  DOWNLOAD_PROJECT,
  onlineEditConfirmMessage,
  PUSH,
  PUSH_ANYWAY,
  readDeployStream,
  refusalButtons,
  serveCliArgs,
  shellLine,
  SHOW_OUTPUT,
} from "./logixDeployLogic";
import { apiFetch } from "./apiFetch";

const CONTEXT_KEY = "nautilus.logixTarget";
const IEC_FILE = /\.(st|ld|fbd|sfc)$/i;

function runtimeUrl(): string {
  return vscode.workspace
    .getConfiguration("nautilus")
    .get<string>("runtimeUrl", "http://localhost:8080")
    .replace(/\/+$/, "");
}

function writeHeaders(): Record<string, string> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  return headers;
}

/** The file the user is in: a text editor, or a diagram custom editor. */
function activeFileUri(): vscode.Uri | undefined {
  const doc = vscode.window.activeTextEditor?.document;
  if (doc && doc.uri.scheme === "file") return doc.uri;
  const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  if (input instanceof vscode.TabInputCustom && input.uri.scheme === "file") return input.uri;
  return undefined;
}

export class LogixDeploy implements vscode.Disposable {
  private out = vscode.window.createOutputChannel("nautilus Logix");
  private disposables: vscode.Disposable[] = [];
  private running = false;

  constructor(private readonly online: OnlineEdit) {
    // The editor-title buttons show for a file whose project declares a
    // Logix target (package.json `when: nautilus.logixTarget`).
    const refresh = () => void this.refreshContext();
    const watcher = vscode.workspace.createFileSystemWatcher("**/nautilus.yaml");
    this.disposables.push(
      this.out,
      watcher,
      watcher.onDidChange(refresh),
      watcher.onDidCreate(refresh),
      watcher.onDidDelete(refresh),
      vscode.window.onDidChangeActiveTextEditor(refresh),
      vscode.window.tabGroups.onDidChangeTabs(refresh)
    );
    refresh();
  }

  private lastKey: boolean | undefined;
  private async refreshContext(): Promise<void> {
    const uri = activeFileUri();
    // Only an IEC file or the manifest decides; anything else (an output
    // pane, a settings file) leaves the buttons as they were.
    if (!uri || !(IEC_FILE.test(uri.path) || path.posix.basename(uri.path) === "nautilus.yaml")) return;
    const on = await this.projectIsLogix(uri);
    if (on !== this.lastKey) {
      this.lastKey = on;
      void vscode.commands.executeCommand("setContext", CONTEXT_KEY, on);
    }
  }

  private async projectIsLogix(file: vscode.Uri): Promise<boolean> {
    const dir = await projectDirFor(file);
    try {
      const raw = await vscode.workspace.fs.readFile(vscode.Uri.joinPath(dir, "nautilus.yaml"));
      return declaresLogixTarget(new TextDecoder().decode(raw));
    } catch {
      return false;
    }
  }

  /** GET /api/program, answered by `naut logix serve --project` — or why
   * not, with the way out offered. */
  private async logixInfo(dir: vscode.Uri, action: string): Promise<LogixInfo | undefined> {
    const url = runtimeUrl();
    const START = "Start naut logix serve";
    let res: Awaited<ReturnType<typeof fetch>>;
    try {
      res = await apiFetch(url + "/api/program", { signal: AbortSignal.timeout(5000) });
    } catch {
      const pick = await notifyError(
        `nautilus: nothing answers at ${url} — ${action} goes through \`naut logix serve --project\`, which also serves the live values`,
        START
      );
      if (pick === START) this.runInTerminal("naut logix serve", serveCliArgs(dir.fsPath, url));
      return undefined;
    }
    const body = (await res.json().catch(() => ({}))) as LogixInfo & { error?: string };
    if (res.status === 404 && /Logix/.test(body.error ?? "")) {
      const pick = await notifyError(
        `nautilus: the \`naut logix serve\` at ${url} was started without --project, so it can serve live values but not deploy. ` +
          "Stop it and start it with --project.",
        START
      );
      if (pick === START) this.runInTerminal("naut logix serve", serveCliArgs(dir.fsPath, url));
      return undefined;
    }
    if (!res.ok) {
      void notifyError(`nautilus: ${url} answered ${res.status} — ${body.error ?? res.statusText}`);
      return undefined;
    }
    if (body.target !== "logix") {
      void notifyError(
        `nautilus: ${url} is not a Logix target — ${action} needs \`naut logix serve --project\` from a naut newer ` +
          'than 0.17.0 (a nautilus controller takes "nautilus: Download Program to Controller")'
      );
      return undefined;
    }
    return body;
  }

  /** Push the program as an online edit: the routine's rungs replaced and
   * finalized in the running controller, which keeps its mode. */
  async onlineEdit(): Promise<void> {
    const prog = await this.online.programForLogix("push as an online edit");
    if (!prog) return;
    const info = await this.logixInfo(prog.dir, "an online edit");
    if (!info) return;
    if (vscode.workspace.getConfiguration("nautilus").get<boolean>("confirmControllerWrites", true)) {
      const pick = await notifyWarning(onlineEditConfirmMessage(info, prog.programFile), { modal: true }, PUSH);
      if (pick !== PUSH) return;
    }
    await this.deploy("online", info, prog, { baseHash: info.hash });
  }

  /** Download the project: stops the controller. Always asks first. */
  async download(): Promise<void> {
    const prog = await this.online.programForLogix("download");
    if (!prog) return;
    const info = await this.logixInfo(prog.dir, "a download");
    if (!info) return;
    if (!info.canDownload) {
      void notifyError(`nautilus: the server at ${runtimeUrl()} does not take downloads — run \`naut logix deploy --download --yes\``);
      return;
    }
    const resetValues = vscode.workspace.getConfiguration("nautilus").get<boolean>("logix.resetValuesOnDownload", false);
    const pick = await notifyWarning(
      downloadConfirmMessage(info, prog.programFile, resetValues),
      { modal: true },
      DOWNLOAD,
      DOWNLOAD_PROGRAM_MODE
    );
    if (pick !== DOWNLOAD && pick !== DOWNLOAD_PROGRAM_MODE) return;
    await this.deploy("download", info, prog, { yes: true, programMode: pick === DOWNLOAD_PROGRAM_MODE, resetValues });
  }

  private async deploy(
    mode: DeployMode,
    info: LogixInfo,
    prog: { programFile: string; programBody: string; dir: vscode.Uri },
    extra: Record<string, unknown>
  ): Promise<void> {
    if (this.running) {
      void notifyWarning("nautilus: a Logix deploy is already running — see the nautilus Logix output");
      return;
    }
    this.running = true;
    const what = mode === "download" ? "download" : "online edit";
    const name = info.controller || "the Logix controller";
    this.out.appendLine(`── ${new Date().toLocaleTimeString()} ${what}: ${prog.programFile} → ${name}${info.commPath ? " (" + info.commPath + ")" : ""}`);
    let final: DeployEvent | undefined;
    try {
      final = await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: `nautilus: ${what} → ${name}` },
        async (progress) => {
          const res = await apiFetch(runtimeUrl() + "/api/logix/deploy", {
            method: "POST",
            headers: writeHeaders(),
            body: JSON.stringify({ mode, source: prog.programBody, ...extra }),
          });
          const reader = res.body?.getReader();
          const dec = new TextDecoder();
          const read = async (): Promise<string | undefined> => {
            if (!reader) return undefined;
            const { done, value } = await reader.read();
            return done ? undefined : dec.decode(value, { stream: true });
          };
          // The result ends the notification at once; a download's stream
          // goes on (the online session for the next edit opening), and
          // what it says goes to the output channel only.
          const { result } = await readDeployStream(read, (message, after) => {
            this.out.appendLine("  " + message);
            if (!after) progress.report({ message });
          });
          let last = result;
          if (!last && !res.ok) last = { event: "error", status: res.status, error: res.statusText };
          if (last?.event === "error" && last.status === undefined) last.status = res.status;
          return last;
        }
      );
    } catch (e) {
      final = { event: "error", error: String(e) };
    } finally {
      this.running = false;
    }

    if (final?.event === "done") {
      const msg = doneSummary(mode, info, final);
      this.out.appendLine("  " + msg);
      void notifyInfo(msg);
      return;
    }
    const err = final?.event === "error" ? final : { event: "error" as const, error: "the server closed the stream without a result" };
    this.out.appendLine("  FAILED: " + err.error);
    if (err.detail) this.out.appendLine("  the SDK said: " + err.detail.replace(/\n/g, "\n  "));
    const buttons = refusalButtons(mode, err, extra.programMode === true);
    // A status is the server's answer; without one the request never got one.
    const verdict = err.status !== undefined ? "refused" : "failed";
    const pick = await notifyError(`nautilus: ${what} ${verdict} — ${err.error}`, ...buttons);
    if (pick === SHOW_OUTPUT) this.out.show(true);
    else if (pick === PUSH_ANYWAY) await this.deploy("online", info, prog, {});
    else if (pick === DOWNLOAD_PROJECT) await this.download();
    // The user has already said yes to a download that stops the
    // controller; this one switches it to Program mode first.
    else if (pick === DOWNLOAD_PROGRAM_MODE) await this.deploy("download", info, prog, { ...extra, programMode: true });
  }

  /** Run a naut verb in a VS Code terminal, which still never leaves the
   * editor. */
  private runInTerminal(name: string, args: string[]): void {
    const term = vscode.window.createTerminal({ name });
    term.show();
    term.sendText(shellLine(cliCommand(), args, process.platform === "win32"));
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
  }
}
