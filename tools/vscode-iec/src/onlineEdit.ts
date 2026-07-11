// PLC-style online edits: push the workspace's ST program to a running
// nautilus controller (warm swap — retained state carries over), diff what
// the controller is running against the workspace, and roll back the last
// push. Talks to the runtime's program API:
//
//   GET  <runtimeUrl>/api/program           running source + hash + dirty
//   PUT  <runtimeUrl>/api/program           {source, baseHash} → swap
//   POST <runtimeUrl>/api/program/rollback  one-step stateful undo
//
// The controller must opt in (server.Options.OnlineEdits) — production
// controllers keep it off. Edits are ephemeral: a restart reverts to the
// deployed program; committing the file is what makes an edit permanent.
//
// Program composition mirrors the runtime and the language server's project
// rule (internal/stproject): sibling .st files with no PROGRAM are libraries
// and precede the program file, sorted by name.

import * as vscode from "vscode";

type ProgramInfo = {
  source: string;
  hash: string;
  dirty: boolean;
  editable: boolean;
  canRollback: boolean;
  error?: string;
};

const REMOTE_SCHEME = "nautilus-controller";
const LOCAL_SCHEME = "nautilus-workspace";
const POLL_MS = 3000;

export class OnlineEdit implements vscode.Disposable {
  private status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 89);
  private timer: NodeJS.Timeout;
  private remoteSource = "";
  private localSource = "";
  private disposables: vscode.Disposable[] = [];

  constructor() {
    this.status.command = "nautilus.program.diff";
    this.timer = setInterval(() => void this.refreshStatus(), POLL_MS);

    const provider: vscode.TextDocumentContentProvider = {
      provideTextDocumentContent: (uri) =>
        uri.scheme === REMOTE_SCHEME ? this.remoteSource : this.localSource,
    };
    this.disposables.push(
      vscode.workspace.registerTextDocumentContentProvider(REMOTE_SCHEME, provider),
      vscode.workspace.registerTextDocumentContentProvider(LOCAL_SCHEME, provider)
    );
    void this.refreshStatus();
  }

  private runtimeUrl(): string {
    return vscode.workspace
      .getConfiguration("nautilus")
      .get<string>("runtimeUrl", "http://localhost:8080")
      .replace(/\/+$/, "");
  }

  private async fetchInfo(): Promise<ProgramInfo | undefined> {
    try {
      const res = await fetch(this.runtimeUrl() + "/api/program");
      if (!res.ok) return undefined;
      return (await res.json()) as ProgramInfo;
    } catch {
      return undefined;
    }
  }

  /**
   * Compose the project source the way the runtime does (stproject.Join):
   * library .st files (no PROGRAM) in the program file's directory, sorted by
   * name and each ended with a newline, then the program file. Open editor
   * buffers win over on-disk content. Returns the composed source, the
   * prelude (composed minus program body — the split prefix), and the program
   * file's URI + body.
   */
  private async compose(): Promise<
    { source: string; prelude: string; programFile: string; programUri: vscode.Uri; programBody: string } | undefined
  > {
    const active = vscode.window.activeTextEditor?.document;
    let dir: vscode.Uri | undefined;
    if (active && active.languageId === "iec-st" && active.uri.scheme === "file") {
      dir = vscode.Uri.joinPath(active.uri, "..");
    } else if (vscode.workspace.workspaceFolders?.length) {
      dir = vscode.workspace.workspaceFolders[0].uri;
    }
    if (!dir) return undefined;

    const entries = await vscode.workspace.fs.readDirectory(dir);
    const stFiles = entries
      .filter(([name, kind]) => kind === vscode.FileType.File && /\.st$/i.test(name))
      .map(([name]) => name)
      .sort();

    const contents = new Map<string, string>();
    for (const name of stFiles) {
      const uri = vscode.Uri.joinPath(dir, name);
      const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
      contents.set(name, open ? open.getText() : new TextDecoder().decode(await vscode.workspace.fs.readFile(uri)));
    }

    const isProgram = (src: string) => /^\s*PROGRAM\b/m.test(src);
    const programs = stFiles.filter((n) => isProgram(contents.get(n) ?? ""));
    if (programs.length === 0) {
      void vscode.window.showErrorMessage("nautilus: no .st file with a PROGRAM found in " + dir.fsPath);
      return undefined;
    }
    let programFile = programs[0];
    if (programs.length > 1) {
      const activeName = active ? active.uri.path.split("/").pop() ?? "" : "";
      if (programs.includes(activeName)) programFile = activeName;
      else {
        void vscode.window.showErrorMessage(
          `nautilus: multiple program files (${programs.join(", ")}) — open the one to download`
        );
        return undefined;
      }
    }

    let prelude = "";
    for (const name of stFiles) {
      const src = contents.get(name);
      if (name === programFile || !src || isProgram(src)) continue;
      prelude += src.endsWith("\n") ? src : src + "\n";
    }
    const programBody = contents.get(programFile) ?? "";
    return {
      source: prelude + programBody,
      prelude,
      programFile,
      programUri: vscode.Uri.joinPath(dir, programFile),
      programBody,
    };
  }

  /** Push the composed workspace program to the controller (warm swap). */
  async download(): Promise<void> {
    const composed = await this.compose();
    if (!composed) return;
    const info = await this.fetchInfo();
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    if (!info.editable) {
      void vscode.window.showErrorMessage(
        "nautilus: this controller has online edits disabled (server.Options.OnlineEdits)"
      );
      return;
    }
    try {
      const res = await fetch(this.runtimeUrl() + "/api/program", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source: composed.source, baseHash: info.hash }),
      });
      const body = (await res.json()) as { hash?: string; resets?: string[]; error?: string };
      if (res.status === 409) {
        const pick = await vscode.window.showWarningMessage(
          "nautilus: controller program changed under you — " + (body.error ?? ""),
          "Force download",
          "Show diff"
        );
        if (pick === "Force download") {
          await this.put(composed.source);
        } else if (pick === "Show diff") {
          await this.diff();
        }
        return;
      }
      if (!res.ok) {
        void vscode.window.showErrorMessage("nautilus: download rejected — " + (body.error ?? res.statusText));
        return;
      }
      const resets = body.resets?.length ? ` · reset: ${body.resets.join(", ")}` : " · all state carried";
      void vscode.window.showInformationMessage(
        `nautilus: online edit live (${body.hash})${resets} — commit the file to keep it`
      );
    } catch (e) {
      void vscode.window.showErrorMessage("nautilus: download failed — " + String(e));
    }
    void this.refreshStatus();
  }

  private async put(source: string): Promise<void> {
    const res = await fetch(this.runtimeUrl() + "/api/program", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source }),
    });
    const body = (await res.json()) as { hash?: string; error?: string };
    if (res.ok) {
      void vscode.window.showInformationMessage(`nautilus: online edit live (${body.hash})`);
    } else {
      void vscode.window.showErrorMessage("nautilus: download rejected — " + (body.error ?? res.statusText));
    }
    void this.refreshStatus();
  }

  /** Side-by-side: what the controller runs vs the composed workspace. */
  async diff(): Promise<void> {
    const info = await this.fetchInfo();
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    const composed = await this.compose();
    this.remoteSource = info.source;
    this.localSource = composed?.source ?? "";
    const remote = vscode.Uri.parse(`${REMOTE_SCHEME}:/controller.st?${Date.now()}`);
    const local = vscode.Uri.parse(`${LOCAL_SCHEME}:/workspace.st?${Date.now()}`);
    await vscode.commands.executeCommand(
      "vscode.diff",
      remote,
      local,
      `nautilus: controller (${info.hash}${info.dirty ? " · online edit" : ""}) ↔ workspace`
    );
  }

  /**
   * Pull the controller's running program into the workspace program file —
   * the inverse of download, so a field online-edit can be reviewed and
   * committed. Only the program file is rewritten; generated type files are
   * never touched. Shows the change and asks before saving.
   */
  async pull(): Promise<void> {
    const info = await this.fetchInfo();
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    const composed = await this.compose();
    if (!composed) return;

    const program = splitProgram(info.source, composed.prelude);
    if (program === undefined) {
      void vscode.window.showErrorMessage(
        "nautilus: the controller's type/library sources differ from this project — " +
          "re-run `nautilus eip import` to reconcile the generated types before pulling the program."
      );
      return;
    }
    if (program === composed.programBody) {
      void vscode.window.showInformationMessage(`nautilus: ${composed.programFile} already matches the controller`);
      return;
    }

    // Preview the incoming change before writing.
    this.remoteSource = program;
    this.localSource = composed.programBody;
    const remote = vscode.Uri.parse(`${REMOTE_SCHEME}:/incoming.st?${Date.now()}`);
    const local = vscode.Uri.parse(`${LOCAL_SCHEME}:/current.st?${Date.now()}`);
    await vscode.commands.executeCommand(
      "vscode.diff",
      local,
      remote,
      `nautilus: ${composed.programFile} (workspace ↔ controller ${info.hash})`
    );
    const pick = await vscode.window.showWarningMessage(
      `Overwrite ${composed.programFile} with the controller's program?`,
      { modal: true },
      "Pull and overwrite"
    );
    if (pick !== "Pull and overwrite") return;

    await vscode.workspace.fs.writeFile(composed.programUri, new TextEncoder().encode(program));
    void vscode.window.showInformationMessage(
      `nautilus: pulled ${composed.programFile} from controller — review the diff and commit to keep it`
    );
    void this.refreshStatus();
  }

  /** One-step stateful undo of the last download. */
  async rollback(): Promise<void> {
    try {
      const res = await fetch(this.runtimeUrl() + "/api/program/rollback", { method: "POST" });
      const body = (await res.json()) as { hash?: string; error?: string };
      if (res.ok) {
        void vscode.window.showInformationMessage(`nautilus: rolled back to ${body.hash}`);
      } else {
        void vscode.window.showWarningMessage("nautilus: rollback — " + (body.error ?? res.statusText));
      }
    } catch (e) {
      void vscode.window.showErrorMessage("nautilus: rollback failed — " + String(e));
    }
    void this.refreshStatus();
  }

  // ── sync status ─────────────────────────────────────────────────────────

  private async refreshStatus(): Promise<void> {
    const stVisible = vscode.window.visibleTextEditors.some((e) => e.document.languageId === "iec-st");
    if (!stVisible) {
      this.status.hide();
      return;
    }
    const info = await this.fetchInfo();
    if (!info) {
      this.status.hide();
      return;
    }
    const composed = await this.compose();
    const inSync = composed ? normalize(composed.source) === normalize(info.source) : false;
    if (inSync && !info.dirty) {
      this.status.hide(); // running exactly what was deployed — nothing to say
      return;
    }
    if (inSync && info.dirty) {
      this.status.text = "$(edit) nautilus: online edit active";
      this.status.tooltip =
        "The controller runs your latest download (matches the workspace) but not what it booted with.\n" +
        "Commit the file to keep it — a controller restart reverts. Click to diff.";
    } else {
      this.status.text = "$(cloud-upload) nautilus: program differs";
      this.status.tooltip =
        "The controller is running a different program than the workspace. Click to diff, " +
        "then Download Program to Controller to push.";
    }
    this.status.show();
  }

  dispose(): void {
    clearInterval(this.timer);
    this.status.dispose();
    for (const d of this.disposables) d.dispose();
  }
}

/** Recover the program body from composed source given the prelude Join
 * placed ahead of it — the TypeScript mirror of stproject.SplitProgram.
 * Returns undefined when the prelude isn't a prefix (the controller's
 * libraries don't match this project). */
function splitProgram(composed: string, prelude: string): string | undefined {
  if (composed.startsWith(prelude)) return composed.slice(prelude.length);
  const trimmed = prelude.replace(/\n+$/, "");
  if (trimmed !== prelude && composed.startsWith(trimmed)) {
    return composed.slice(trimmed.length).replace(/^\n/, "");
  }
  return undefined;
}

/** Whitespace-insensitive comparison: embed order and blank lines differ
 * between a binary's embed composition and the editor's, but the logic
 * doesn't. */
function normalize(src: string): string {
  return src
    .replace(/\r/g, "")
    .split("\n")
    .map((l) => l.replace(/\s+$/g, ""))
    .filter((l) => l.length > 0)
    .join("\n");
}
